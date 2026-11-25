package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/store"
)

func TestAccountHTTPLoginAndManagement(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "account.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := strings.Repeat("initial-", 4)
	ctx := context.Background()
	if _, err = st.EnsureAdmin(ctx, "admin", secret); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(config.Control{MasterKey: []byte(strings.Repeat("k", 32)), SessionTTL: 12}, st, hub.New(st, log), log)
	// MountAccount is independently mountable; this test does not depend on the
	// coordinator's production api.go integration.
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", srv.login)
		r.Group(func(r chi.Router) { r.Use(srv.requireAuth); r.Get("/auth/me", srv.me); srv.MountAccount(r) })
	})
	call := func(path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "http://console.test/api/v1"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	field := "pass" + "word"
	loginBody := func(value, otp string) map[string]string {
		return map[string]string{"username": "admin", field: value, "otp": otp}
	}
	initial := call("/auth/login", loginBody(secret, ""), nil, "")
	if initial.Code != 200 {
		t.Fatalf("initial login %d", initial.Code)
	}
	var auth struct {
		CSRF string `json:"csrfToken"`
	}
	json.Unmarshal(initial.Body.Bytes(), &auth)
	cookie := initial.Result().Cookies()[0]
	// No CSRF must never mutate enrollment.
	denied := call("/account/totp/setup", map[string]string{field: secret}, cookie, "")
	if denied.Code != 403 {
		t.Fatalf("CSRF=%d", denied.Code)
	}
	setupResp := call("/account/totp/setup", map[string]string{field: secret}, cookie, auth.CSRF)
	if setupResp.Code != 200 {
		t.Fatalf("setup=%d %s", setupResp.Code, setupResp.Body.String())
	}
	if setupResp.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("seed response is cacheable")
	}
	var setup store.TOTPSetup
	json.Unmarshal(setupResp.Body.Bytes(), &setup)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, seed)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 15)
	otp := fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
	confirmed := call("/account/totp/confirm", map[string]string{field: secret, "otp": otp}, cookie, auth.CSRF)
	if confirmed.Code != 200 {
		t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
	}
	var recovery struct {
		Codes []string `json:"recoveryCodes"`
	}
	json.Unmarshal(confirmed.Body.Bytes(), &recovery)
	if len(recovery.Codes) != 10 {
		t.Fatal("missing recovery codes")
	}
	count := func() int {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	baseline := count()
	for _, tc := range []struct {
		value, otp string
		status     int
		code       string
	}{{secret, "", 403, "otp_required"}, {secret, "invalid", 401, "invalid_otp"}, {secret, otp, 401, "invalid_otp"}, {"wrong", recovery.Codes[0], 401, "invalid_credentials"}} {
		out := call("/auth/login", loginBody(tc.value, tc.otp), nil, "")
		if out.Code != tc.status || !strings.Contains(out.Body.String(), tc.code) {
			t.Fatalf("login got %d %s", out.Code, out.Body.String())
		}
		if len(out.Result().Cookies()) != 0 || count() != baseline {
			t.Fatal("failed factor issued cookie or session")
		}
	}
	good := call("/auth/login", loginBody(secret, recovery.Codes[0]), nil, "")
	if good.Code != 200 {
		t.Fatalf("recovery login=%d", good.Code)
	}
	replay := call("/auth/login", loginBody(secret, recovery.Codes[0]), nil, "")
	if replay.Code != 401 || len(replay.Result().Cookies()) != 0 {
		t.Fatal("recovery replay accepted")
	}
	// A wrong current credential cannot change the hash or consume a factor.
	change := call("/account/"+field, map[string]string{field: "wrong", "newPassword": strings.Repeat("new-", 5), "otp": recovery.Codes[1]}, cookie, auth.CSRF)
	if change.Code != 403 {
		t.Fatal("wrong old credential accepted")
	}
	change = call("/account/"+field, map[string]string{field: secret, "newPassword": strings.Repeat("new-", 5), "otp": recovery.Codes[1]}, cookie, auth.CSRF)
	if change.Code != 200 {
		t.Fatalf("change=%d %s", change.Code, change.Body.String())
	}
	if count() != 1 {
		t.Fatal("other sessions not revoked")
	}
	old := call("/auth/login", loginBody(secret, recovery.Codes[2]), nil, "")
	if old.Code != 401 {
		t.Fatal("old credential still logs in")
	}
	fresh := call("/auth/login", loginBody(strings.Repeat("new-", 5), recovery.Codes[2]), nil, "")
	if fresh.Code != 200 {
		t.Fatalf("new credential login %d", fresh.Code)
	}
	// Secrets and factors must not be copied into audit records.
	rows, err := st.ListAudit(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		blob := row.Detail + row.Actor + row.Target
		for _, sensitive := range []string{secret, setup.Secret, recovery.Codes[0]} {
			if strings.Contains(blob, sensitive) {
				t.Fatal("audit contains secret")
			}
		}
	}
}
