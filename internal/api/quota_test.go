package api

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"strings"
	"testing"
	"time"

	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

func TestQuotaControlProtocol(t *testing.T) {
	env := newTestEnv(t)
	n, key, err := env.store.CreateNode(context.Background(), model.Node{Name: "quota", LimitTotalGB: 0.00000001, LimitKind: "monthly", LimitResetDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.srv.URL, "http")+"/node/ws", &websocket.DialOptions{HTTPHeader: http.Header{"X-Node-Id": {n.ID}, "Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","version":"test"}`)); err != nil {
		t.Fatal(err)
	}
	var config model.AgentFrame
	if err = json.Unmarshal(readUntilType(t, ctx, conn, "config"), &config); err != nil {
		t.Fatal(err)
	}
	if config.NodePolicy == nil || config.NodePolicy.LimitTotalGB != n.LimitTotalGB || config.NodePolicy.LimitKind != "monthly" {
		t.Fatalf("missing quota policy: %+v", config.NodePolicy)
	}
	for _, tc := range []struct {
		ref  string
		want int64
	}{{"0123456789abcdef0123456789abcdef", 10}, {"0123456789abcdef0123456789abcdef", 10}, {"1123456789abcdef0123456789abcdef", 0}} {
		body, _ := json.Marshal(model.AgentFrame{Type: "quota_request", Ref: tc.ref, TrafficBytes: 20})
		if err = conn.Write(ctx, websocket.MessageText, body); err != nil {
			t.Fatal(err)
		}
		var ack model.AgentFrame
		if err = json.Unmarshal(readUntilType(t, ctx, conn, "quota_ack"), &ack); err != nil {
			t.Fatal(err)
		}
		if !ack.OK || ack.Ref != tc.ref || ack.TrafficBytes != tc.want {
			t.Fatalf("unexpected quota ack %+v", ack)
		}
	}
}

func TestNodeQuotaAPIValidationAndStatus(t *testing.T) {
	env := newTestEnv(t)
	for _, body := range []map[string]any{
		{"name": "bad", "limitTotalGb": -1},
		{"name": "bad", "limitKind": "daily"},
		{"name": "bad", "limitResetDay": 32},
	} {
		resp := env.do(t, http.MethodPost, "/api/v1/nodes", body, true)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid quota accepted: %d", resp.StatusCode)
		}
	}
	n, _, err := env.store.CreateNode(context.Background(), model.Node{Name: "quota", LimitTotalGB: 0.000001})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = env.store.ReserveNodeQuota(context.Background(), n.ID, "0123456789abcdef0123456789abcdef", 73); err != nil {
		t.Fatal(err)
	}
	resp := env.do(t, http.MethodGet, "/api/v1/nodes/"+n.ID+"/quota", nil, false)
	defer resp.Body.Close()
	var status store.QuotaStatus
	if err = json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || status.LimitBytes != 1000 || status.ReservedBytes != 73 || status.RemainingBytes != 927 {
		t.Fatalf("status %d %+v", resp.StatusCode, status)
	}
	resp = env.do(t, http.MethodGet, "/api/v1/nodes/missing/quota", nil, false)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("missing status %d", resp.StatusCode)
	}
}
