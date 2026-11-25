package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

func TestNATControlAuthorization(t *testing.T) {
	rt := NewRuntime("n", "127.0.0.1:0", "127.0.0.1", nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.RendezvousListen = "127.0.0.1:0"
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	secret := bytes.Repeat([]byte{9}, 32)
	f := model.TunnelForward{ID: "f", Kind: "p2p", DeviceID: "provider", ClientID: "visitor", Revision: 7, Protocol: "tcp"}
	rt.ApplyConfig(base64.RawURLEncoding.EncodeToString(secret), []model.TunnelForward{f})
	connect := func(device string) *yamux.Session {
		t.Helper()
		conn, err := net.Dial("tcp", rt.tunnelLn.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := tunnel.Sign(secret, tunnel.Ticket{DeviceID: device, NodeID: "n", Forwards: []string{"f"}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_ = tunnel.WriteAuth(conn, tunnel.AuthRequest{DeviceID: device, Ticket: ticket})
		status, _, err := tunnel.ReadResult(conn)
		if err != nil || status != tunnel.StatusOK {
			t.Fatalf("auth %d %v", status, err)
		}
		_ = conn.SetDeadline(time.Time{})
		session, err := tunnel.ServerSession(conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	provider := connect("provider")
	visitor := connect("visitor")
	outsider := connect("outsider")
	_, pin, err := tunnel.DirectCertificate()
	if err != nil {
		t.Fatal(err)
	}
	request := func(session *yamux.Session, id string, publish bool) (tunnel.NATCredential, error) {
		var result tunnel.NATCredential
		stream, e := session.OpenStream()
		if e != nil {
			return result, e
		}
		defer stream.Close()
		_ = stream.SetDeadline(time.Now().Add(time.Second))
		if e = tunnel.WriteForwardID(stream, id); e != nil {
			return result, e
		}
		if publish {
			_ = json.NewEncoder(stream).Encode(tunnel.NATCredential{Fingerprint: pin})
		}
		status, _, e := tunnel.ReadResult(stream)
		if e != nil {
			return result, e
		}
		if status != tunnel.StatusOK {
			return result, tunnel.ErrNATUnavailable
		}
		e = json.NewDecoder(stream).Decode(&result)
		return result, e
	}
	if _, err = request(outsider, "@nat:7:f", true); err == nil {
		t.Fatal("unrelated device got credential")
	}
	if _, err = request(provider, "@nat:6:f", true); err == nil {
		t.Fatal("stale revision got credential")
	}
	pc, err := request(provider, "@nat:7:f", true)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := request(visitor, "@nat:7:f", false)
	if err != nil {
		t.Fatal(err)
	}
	if pc.ID == vc.ID || pc.Key == vc.Key || vc.Fingerprint != pin {
		t.Fatal("role credentials or provider pin invalid")
	}
	for name, policy := range map[string]model.NodeRuntimePolicy{
		"quota": {LimitTotalGB: 1}, "bandwidth": {LimiterMbps: 1}, "concurrency": {Concurrency: 1}, "connections": {Connections: 1},
	} {
		t.Run(name, func(t *testing.T) {
			rt.mu.Lock()
			rt.policy = policy
			rt.mu.Unlock()
			if _, err = request(visitor, "@nat:7:f", false); err == nil {
				t.Fatal("limited node allowed direct NAT")
			}
		})
	}
	rt.mu.Lock()
	rt.policy = model.NodeRuntimePolicy{}
	rt.mu.Unlock()
	rt.RevokeDevice("provider", 0)
	if _, err = request(visitor, "@nat:7:f", false); err == nil {
		t.Fatal("revoked provider still discoverable")
	}
}
