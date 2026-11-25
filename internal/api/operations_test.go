package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"server-aibot/internal/store"
)

func TestOperationsCRUDSecurityAndReports(t *testing.T) {
	e := newTestEnv(t)
	response, err := http.Get(e.srv.URL + "/api/v1/checks")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("anonymous list=%d", response.StatusCode)
	}
	asset, err := e.store.CreateAsset(context.Background(), store.Asset{Name: "registered", Address: "127.0.0.1", Port: 1, Protocol: "tcp", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	input := store.OperationCheck{Name: "local closed port", Kind: "tcp", TargetKind: "asset", TargetID: asset.ID, TimeoutMS: 100, IntervalSeconds: 30, Enabled: false}
	response = e.do(t, "POST", "/api/v1/checks", input, false)
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("csrf bypass=%d", response.StatusCode)
	}
	response = e.do(t, "POST", "/api/v1/checks", input, true)
	if response.StatusCode != 201 {
		t.Fatalf("create=%d", response.StatusCode)
	}
	check := decode[store.OperationCheck](t, response)
	// Invalid references cannot become arbitrary dial targets.
	invalid := input
	invalid.TargetID = "missing"
	response = e.do(t, "POST", "/api/v1/checks", invalid, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("invalid target=%d", response.StatusCode)
	}
	response = e.do(t, "POST", "/api/v1/notify/channels", map[string]any{"name": "bot", "kind": "webhook", "enabled": false, "webhookUrl": "https://127.0.0.1/metadata"}, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("private callback=%d", response.StatusCode)
	}
	response = e.do(t, "POST", "/api/v1/notify/channels", map[string]any{"name": "bot", "kind": "webhook", "enabled": false, "webhookUrl": "https://example.com/secret-token"}, true)
	if response.StatusCode != 201 {
		t.Fatalf("channel create=%d", response.StatusCode)
	}
	channel := decode[store.NotificationChannel](t, response)
	response = e.do(t, "GET", "/api/v1/notify/channels", nil, false)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "example.com") || strings.Contains(string(body), "secret-token") {
		t.Fatal("list leaked callback URL")
	}
	// Disabled channels remain configurable without resubmitting their secret.
	response = e.do(t, "PUT", "/api/v1/notify/channels/"+channel.ID, channel, true)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("channel edit=%d", response.StatusCode)
	}
	// Use store completion to test API history without any real network activity.
	if _, err = e.store.FinishOperationCheck(context.Background(), check, "down", "test failure", 5); err != nil {
		t.Fatal(err)
	}
	response = e.do(t, "GET", "/api/v1/inspection-runs", nil, false)
	runs := decode[struct {
		Items []store.InspectionRun `json:"items"`
	}](t, response)
	if len(runs.Items) != 1 || runs.Items[0].Score == nil || *runs.Items[0].Score != 0 {
		t.Fatalf("reports=%+v", runs)
	}
	response = e.do(t, "GET", "/api/v1/alerts", nil, false)
	alerts := decode[struct {
		Items []store.OperationAlert `json:"items"`
	}](t, response)
	if len(alerts.Items) != 1 {
		t.Fatal("missing alert")
	}
	response = e.do(t, "POST", "/api/v1/alerts/"+alerts.Items[0].ID+"/ack", nil, true)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("ack=%d", response.StatusCode)
	}
	response = e.do(t, "POST", "/api/v1/alerts/"+alerts.Items[0].ID+"/silence", map[string]int{"seconds": 3600}, true)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("silence=%d", response.StatusCode)
	}
	response = e.do(t, "DELETE", "/api/v1/checks/"+check.ID, nil, true)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete=%d", response.StatusCode)
	}
	response = e.do(t, "GET", "/api/v1/inspection-runs", nil, false)
	runs = decode[struct {
		Items []store.InspectionRun `json:"items"`
	}](t, response)
	if len(runs.Items) != 1 {
		t.Fatal("delete lost historical report")
	}
}
