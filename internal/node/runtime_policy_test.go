package node

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
	"testing"
	"time"
)

func TestRevokeClosesEntryAndRejectsOlderConfig(t *testing.T) {
	r := NewRuntime("node", "127.0.0.1:0", "127.0.0.1", nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	entry, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := entry.Addr().(*net.TCPAddr).Port
	entry.Close()
	f := model.TunnelForward{ID: "f", DeviceID: "d", Kind: "port", RemotePort: port, Revision: 1, Protocol: "tcp"}
	r.ApplyConfiguration(model.AgentFrame{Revision: 1, TunnelSecret: secret, Forwards: []model.TunnelForward{f}})
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &activeEntry{forward: f, conn: a, cancel: cancel}
	r.mu.Lock()
	r.active[e] = true
	r.mu.Unlock()
	acked := false
	r.SetSender(func(frame model.AgentFrame) error {
		if frame.Type == "revoke_result" {
			if frame.Revision != 2 {
				t.Errorf("wrong revision %d", frame.Revision)
			}
			select {
			case <-ctx.Done():
			default:
				t.Error("revocation ack before cancellation")
			}
			_ = b.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := b.Write([]byte("x")); err == nil {
				t.Error("entry still open at revoke ack")
			}
			acked = true
		}
		return nil
	})
	f.Disabled = true
	f.Revision = 2
	r.ApplyConfiguration(model.AgentFrame{Revision: 2, TunnelSecret: secret, Forwards: []model.TunnelForward{f}})
	if !acked {
		t.Fatal("missing revoke ack")
	}
	f.Disabled = false
	f.Revision = 1
	r.ApplyConfiguration(model.AgentFrame{Revision: 1, TunnelSecret: secret, Forwards: []model.TunnelForward{f}})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.listeners) != 0 || len(r.forwards) != 0 {
		t.Fatal("stale config resurrected listener")
	}
}
func TestNodeEnforcesPortPolicyAndUnsupportedProtocol(t *testing.T) {
	r := NewRuntime("node", "127.0.0.1:0", "127.0.0.1", nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	count := 0
	r.SetSender(func(f model.AgentFrame) error {
		if f.Type == "bind_result" {
			count++
			if f.OK {
				t.Error("invalid policy unexpectedly bound")
			}
		}
		return nil
	})
	r.ApplyConfiguration(model.AgentFrame{TunnelSecret: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), NodePolicy: &model.NodeRuntimePolicy{ForwardPorts: "31000-32000"}, Forwards: []model.TunnelForward{{ID: "outside", DeviceID: "d", Kind: "port", Protocol: "tcp", RemotePort: 30000}, {ID: "unsupported", DeviceID: "d", Kind: "port", Protocol: "quic", RemotePort: 31001}}})
	if count != 2 {
		t.Fatalf("rejections %d", count)
	}
}
func TestNodeRejectsForbiddenP2PRelay(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "forbidden"}[disabled], func(t *testing.T) {
			r := NewRuntime("node", "", "", nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
			r.ctx = context.Background()
			f := model.TunnelForward{ID: "f", DeviceID: "provider", ClientID: "visitor", Kind: "p2p", Revision: 1, DisableRelay: disabled}
			r.forwards[f.ID] = f
			pa, pb := net.Pipe()
			provider, err := tunnel.ClientSession(pa)
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			service, err := tunnel.ServerSession(pb)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			r.sessions["provider"] = &deviceTunnel{deviceID: "provider", session: provider, allowed: map[string]bool{"f": true}}
			go func() {
				stream, err := service.AcceptStream()
				if err != nil {
					return
				}
				defer stream.Close()
				if _, err := tunnel.ReadForwardID(stream); err != nil {
					return
				}
				_ = tunnel.WriteResult(stream, tunnel.StatusOK, "")
				_, _ = io.Copy(stream, stream)
			}()
			va, vb := net.Pipe()
			peer, err := tunnel.ClientSession(va)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			visitor, err := tunnel.ServerSession(vb)
			if err != nil {
				t.Fatal(err)
			}
			defer visitor.Close()
			dt := &deviceTunnel{deviceID: "visitor", session: peer, allowed: map[string]bool{"f": true}}
			r.sessions["visitor"] = dt
			go r.acceptPrivateStreams(dt)
			stream, err := visitor.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(time.Second))
			if err := tunnel.WriteForwardID(stream, "@relay:1:f"); err != nil {
				t.Fatal(err)
			}
			status, _, err := tunnel.ReadResult(stream)
			if disabled {
				if err == nil && status == tunnel.StatusOK {
					t.Fatal("node accepted forbidden relay")
				}
			} else if err != nil || status != tunnel.StatusOK {
				t.Fatalf("allowed relay failed: status=%d err=%v", status, err)
			}
		})
	}
}

func TestBandwidthBudgetAndCancellation(t *testing.T) {
	p := bytePacer{}
	ctx := context.Background()
	start := time.Now()
	if err := p.wait(ctx, 12500, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(ctx, 12500, 1); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatal("aggregate Mbps budget not enforced")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.wait(cancelled, 125000, 1); err == nil {
		t.Fatal("cancelled pacing succeeded")
	}
}
