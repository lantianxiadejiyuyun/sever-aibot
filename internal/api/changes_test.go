package api

import (
	"context"
	"testing"

	"server-aibot/internal/store"
)

func TestChangeRoutesApprovalExecutionGateAndTOTP(t *testing.T) {
	t.Setenv("AIBOT_ENABLE_SSH_CHANGES", "")
	e := newTestEnv(t)
	a, err := e.store.CreateAsset(context.Background(), store.Asset{Name: "manual fixture", Address: "127.0.0.1", Port: 1, Protocol: "ssh", Enabled: true, HostKey: "SHA256:fixture", Tags: "managed:manual"})
	if err != nil {
		t.Fatal(err)
	}
	credentialResponse := e.do(t, "POST", "/api/v1/credentials", map[string]string{"name": "change fixture", "kind": "ssh_password", "username": "tester", "secret": "fixture-only"}, true)
	credential := decode[store.CredentialMeta](t, credentialResponse)
	a.CredentialID = credential.ID
	if _, err = e.store.UpdateAsset(context.Background(), *a); err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"assetId": a.ID, "kind": "service_restart", "service": "nginx"}
	resp := e.do(t, "POST", "/api/v1/changes", input, true)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("stepup bypass: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	resp.Body.Close()
	resp = e.do(t, "POST", "/api/v1/changes", input, true)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	p := decode[store.ChangeProposal](t, resp)
	confirm := map[string]any{"revision": p.Revision, "confirmed": true}
	resp = e.do(t, "POST", "/api/v1/changes/"+p.ID+"/execute", confirm, true)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("default-disabled execution: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/changes/"+p.ID+"/approve", confirm, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	t.Setenv("AIBOT_ENABLE_SSH_CHANGES", "1")
	resp = e.do(t, "POST", "/api/v1/changes/"+p.ID+"/execute", confirm, true)
	if resp.StatusCode != 200 {
		t.Fatalf("execute fixture failure: %d", resp.StatusCode)
	}
	run := decode[store.ChangeExecution](t, resp)
	if run.Status != "failed" {
		t.Fatalf("unreachable target result=%s", run.Status)
	}
	resp = e.do(t, "POST", "/api/v1/changes/"+p.ID+"/execute", confirm, true)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("execution replay: %d", resp.StatusCode)
	}
	high := p
	high.Risk = "high"
	high.Kind = "config_update"
	saved, err := e.store.CreateChangeProposal(context.Background(), high)
	if err != nil {
		t.Fatal(err)
	}
	resp = e.do(t, "POST", "/api/v1/changes/"+saved.ID+"/approve", confirm, true)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("high risk approved without TOTP: %d", resp.StatusCode)
	}
	a.Tags = "managed:manual,managed:baota"
	if _, err = e.store.UpdateAsset(context.Background(), *a); err != nil {
		t.Fatal(err)
	}
	resp = e.do(t, "POST", "/api/v1/changes", input, true)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("Baota target accepted: %d", resp.StatusCode)
	}
}
