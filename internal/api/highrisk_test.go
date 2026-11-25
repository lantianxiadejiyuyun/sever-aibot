package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

func TestHighRiskFactorHTTPContract(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "highrisk.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := strings.Repeat("fixture-", 4)
	if _, err = st.EnsureAdmin(ctx, "admin", secret); err != nil {
		t.Fatal(err)
	}
	admin, err := st.Authenticate(ctx, "admin", secret)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := st.CreateSession(ctx, admin.ID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sess.Username = admin.Username
	key := []byte(strings.Repeat("k", 32))
	v, _ := vault.New(key)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Control{MasterKey: key}, st, hub.New(st, log), log)
	call := func(session *store.Session, otp string, status int, code string) bool {
		t.Helper()
		r := httptest.NewRequest("POST", "http://console.test/approve", nil)
		if session != nil {
			r = r.WithContext(context.WithValue(r.Context(), sessionKey, session))
		}
		w := httptest.NewRecorder()
		ok := server.RequireHighRiskFactor(w, r, otp)
		if w.Code != status || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("HTTP got %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return ok
	}
	if call(nil, "123456", 401, "unauthenticated") {
		t.Fatal("anonymous allowed")
	}
	if call(sess, "123456", 403, "totp_required") {
		t.Fatal("disabled factor allowed")
	}
	previous := time.Now().Add(-30 * time.Second)
	setup, err := st.BeginTOTP(ctx, admin, sess.ID, v, previous)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	otpAt := func(at time.Time) string {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(at.Unix()/30))
		m := hmac.New(sha1.New, seed)
		m.Write(b[:])
		sum := m.Sum(nil)
		i := int(sum[len(sum)-1] & 15)
		return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[i:i+4])&0x7fffffff)%1000000)
	}
	codes, err := st.ConfirmTOTP(ctx, admin, sess.ID, otpAt(previous), v, previous)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Minute)
	sess.StepUpUntil = &future
	if call(sess, otpAt(time.Now()), 403, "step_up_required") {
		t.Fatal("cached step-up bypassed database state")
	}
	if err = st.SetStepUp(ctx, sess.ID, future); err != nil {
		t.Fatal(err)
	}
	if call(sess, "", 403, "otp_required") {
		t.Fatal("missing factor allowed")
	}
	if call(sess, codes[0], 403, "invalid_otp") {
		t.Fatal("recovery code allowed")
	}
	code := otpAt(time.Now())
	if !call(sess, code, 200, "") {
		t.Fatal("fresh TOTP rejected")
	}
	if call(sess, code, 403, "invalid_otp") {
		t.Fatal("TOTP replay accepted")
	}
	state, err := st.AccountSecurity(ctx, admin.ID)
	if err != nil || state.RecoveryCodesRemaining != 10 {
		t.Fatal("recovery code consumed")
	}
	rows, err := st.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Detail, code) || strings.Contains(row.Detail, codes[0]) {
			t.Fatal("factor entered audit details")
		}
	}
}
