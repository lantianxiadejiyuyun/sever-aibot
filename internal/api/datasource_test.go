package api

import (
	"context"
	"testing"

	"server-aibot/internal/store"
)

func TestDataSourceAuthenticationBindingAndSQLGate(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, "POST", "/api/v1/datasources", map[string]any{}, true)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("step-up bypass: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("step-up: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/credentials", map[string]string{"name": "DB", "kind": "db_password", "username": "reader", "secret": "fixture-db-secret"}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("credential: %d", resp.StatusCode)
	}
	cred := decode[store.CredentialMeta](t, resp)
	a, err := e.store.CreateAsset(context.Background(), store.Asset{Name: "DB", Address: "127.0.0.1", Port: 1, Protocol: "postgres", Enabled: true, CredentialID: cred.ID})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"name": "source", "assetId": a.ID, "credentialId": cred.ID, "database": "fixture"}
	resp = e.do(t, "POST", "/api/v1/datasources", input, false)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("CSRF bypass: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/datasources", input, true)
	if resp.StatusCode != 201 {
		t.Fatalf("create datasource: %d", resp.StatusCode)
	}
	d := decode[store.DataSource](t, resp)
	if d.TLSMode != "verify-full" {
		t.Fatalf("TLS default: %s", d.TLSMode)
	}
	resp = e.do(t, "POST", "/api/v1/datasources/"+d.ID+"/query", map[string]string{"sql": "DELETE FROM users"}, true)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("write SQL accepted: %d", resp.StatusCode)
	}
	input["credentialId"] = "unrelated"
	resp = e.do(t, "POST", "/api/v1/datasources", input, true)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("unrelated credential accepted: %d", resp.StatusCode)
	}
	resp = e.do(t, "PATCH", "/api/v1/datasources/"+d.ID, map[string]any{"enabled": false}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	resp = e.do(t, "GET", "/api/v1/datasources/"+d.ID+"/schema", nil, false)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("disabled source queried: %d", resp.StatusCode)
	}
	resp = e.do(t, "DELETE", "/api/v1/datasources/"+d.ID, nil, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete source: %d", resp.StatusCode)
	}
}
