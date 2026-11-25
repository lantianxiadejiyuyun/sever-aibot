package api

import (
	"context"
	"server-aibot/internal/model"
	"testing"
)

func TestNodeUDPAddressPersistence(t *testing.T) {
	e := newTestEnv(t)
	for _, protocol := range []string{"kcp", "quic"} {
		r := e.do(t, "POST", "/api/v1/nodes", map[string]any{"name": protocol, "protocol": protocol, "addr": "node.example.com:7000", "rendezvousAddress": "node.example.com:7001"}, true)
		if r.StatusCode != 201 {
			t.Fatalf("create %s: %d", protocol, r.StatusCode)
		}
		v := decode[struct {
			Node model.Node `json:"node"`
		}](t, r)
		n, err := e.store.GetNode(context.Background(), v.Node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n.RendezvousAddress != "node.example.com:7001" || nodeTunnelAddress(n) != protocol+"://node.example.com:7000" {
			t.Fatal(n)
		}
		r = e.do(t, "PATCH", "/api/v1/nodes/"+n.ID, map[string]any{"rendezvousAddress": "127.0.0.1:7002"}, true)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		n, err = e.store.GetNode(context.Background(), n.ID)
		if err != nil || n.RendezvousAddress != "127.0.0.1:7002" {
			t.Fatal(n, err)
		}
	}
	for _, n := range []model.Node{{Protocol: "quic", Addr: "kcp://host:7000"}, {Protocol: "quic", Addr: "quic://host:0"}, {Protocol: "kcp", Addr: "kcp://user:pass@host:7000"}, {RendezvousAddress: "host"}} {
		if validateNodeTransport(n) == nil {
			t.Fatalf("accepted invalid address %+v", n)
		}
	}
}
