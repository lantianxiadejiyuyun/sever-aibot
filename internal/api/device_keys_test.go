package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"server-aibot/internal/model"
	"strings"
	"testing"
	"time"
)

func TestDeviceKeyAPI(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	d, key, err := e.store.CreateDevice(ctx, "legacy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/devices/" + d.ID
	check := func(path string, body any, csrf bool, status int) string {
		t.Helper()
		r := e.do(t, http.MethodPost, path, body, csrf)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != status {
			t.Fatalf("%s status=%d want=%d body=%s", path, r.StatusCode, status, b)
		}
		if r.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return string(b)
	}
	for _, suffix := range []string{"/reveal-key", "/retain-key"} {
		r, err := http.Post(e.srv.URL+base+suffix, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("anonymous secret request accepted")
		}
		check(base+suffix, nil, false, 403)
		if b := check(base+suffix, nil, true, 403); !strings.Contains(b, "step_up_required") {
			t.Fatal(b)
		}
	}
	if _, err = e.store.DB().ExecContext(ctx, `UPDATE admin_sessions SET step_up_until=?`, time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if b := check(base+"/reveal-key", nil, true, 409); !strings.Contains(b, "key_unavailable") {
		t.Fatal(b)
	}
	check(base+"/retain-key", map[string]string{"deviceKey": "wrong"}, true, 409)
	if b := check(base+"/retain-key", map[string]string{"deviceKey": key}, true, 200); !strings.Contains(b, `"ok":true`) {
		t.Fatal(b)
	}
	b := check(base+"/reveal-key", nil, true, 200)
	var revealed map[string]string
	if err = json.Unmarshal([]byte(b), &revealed); err != nil || revealed["deviceId"] != d.ID || revealed["deviceKey"] != key {
		t.Fatal("reveal contract mismatch")
	}
	rotated := check(base+"/rotate-key", nil, true, 200)
	var rotation map[string]string
	if err = json.Unmarshal([]byte(rotated), &rotation); err != nil {
		t.Fatal(err)
	}
	b = check(base+"/reveal-key", nil, true, 200)
	if !strings.Contains(b, rotation["deviceKey"]) || strings.Contains(b, key) {
		t.Fatal("reveal did not follow rotation")
	}
	check(base+"/retain-key", map[string]string{"deviceKey": key}, true, 429)
	check(base+"/reveal-key", nil, true, 429)
	r := e.do(t, http.MethodGet, "/api/v1/devices", nil, false)
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	for _, secret := range []string{key, rotation["deviceKey"], "key_ct", "keyHash", "ciphertext"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("list leaked key material")
		}
	}
	if !strings.Contains(string(data), `"keyRetrievable":true`) {
		t.Fatal("list missing retrievability")
	}
	audit, err := e.store.ListAudit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(audit)
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), rotation["deviceKey"]) {
		t.Fatal("audit leaked secret")
	}
	// Creating through the API must retain the key immediately.
	r = e.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "new"}, true)
	if r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	created := decode[struct {
		Device model.Device `json:"device"`
		Key    string       `json:"deviceKey"`
	}](t, r)
	if !created.Device.KeyRetrievable || created.Key == "" {
		t.Fatal("API creation did not retain key")
	}
}
