package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"server-aibot/internal/model"
	"server-aibot/internal/relay"
	"server-aibot/internal/tunnel"
)

func TestNATFallbackBudget(t *testing.T) {
	a, b := net.Pipe()
	visitor, err := tunnel.ServerSession(a)
	if err != nil {
		t.Fatal(err)
	}
	defer visitor.Close()
	node, err := tunnel.ClientSession(b)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	blackhole, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	go func() {
		for {
			stream, err := node.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer stream.Close()
				id, err := tunnel.ReadForwardID(stream)
				if err != nil {
					return
				}
				if id == "@nat:1:f" {
					_ = tunnel.WriteResult(stream, tunnel.StatusOK, "")
					_ = json.NewEncoder(stream).Encode(tunnel.NATCredential{ID: string(bytes.Repeat([]byte{'a'}, 64)), Key: string(bytes.Repeat([]byte{'b'}, 64)), Fingerprint: string(bytes.Repeat([]byte{'c'}, 64))})
				}
			}()
		}
	}()
	manager := &tunnelManager{}
	start := time.Now()
	conn, err := manager.dialDirect(visitor, model.TunnelForward{ID: "f", Revision: 1, Visitor: true, RendezvousAddress: blackhole.LocalAddr().String()})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("blocked UDP succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2200*time.Millisecond {
		t.Fatalf("NAT blocked fallback for %v", elapsed)
	}
}

func TestNATAgentRoundTripAndRevoke(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rendezvous := tunnel.NewRendezvous(udp)
	defer rendezvous.Close()
	var revoked atomic.Bool
	connect := func(provider bool) *yamux.Session {
		t.Helper()
		a, b := net.Pipe()
		client, e := tunnel.ServerSession(a)
		if e != nil {
			t.Fatal(e)
		}
		server, e := tunnel.ClientSession(b)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { client.Close(); server.Close() })
		go func() {
			for {
				stream, e := server.AcceptStream()
				if e != nil {
					return
				}
				go func() {
					defer stream.Close()
					_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
					id, e := tunnel.ReadForwardID(stream)
					if e != nil || id != "@nat:1:f" {
						return
					}
					pin := ""
					if provider {
						var req tunnel.NATCredential
						if json.NewDecoder(stream).Decode(&req) != nil {
							return
						}
						pin = req.Fingerprint
					} else {
						pin = rendezvous.ProviderPin("f:1")
						if pin == "" {
							_ = tunnel.WriteResult(stream, tunnel.StatusDialFailed, "")
							return
						}
					}
					credential, e := rendezvous.Issue("f:1", provider, pin, func() bool { return !revoked.Load() })
					if e != nil {
						return
					}
					_ = tunnel.WriteResult(stream, tunnel.StatusOK, "")
					_ = json.NewEncoder(stream).Encode(credential)
				}()
			}
		}()
		return client
	}
	providerSession := connect(true)
	visitorSession := connect(false)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, e := target.Accept()
			if e != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	_, port, _ := net.SplitHostPort(target.Addr().String())
	targetPort, _ := strconv.Atoi(port)
	f := model.TunnelForward{ID: "f", Kind: "p2p", Revision: 1, DeviceID: "provider", ClientID: "visitor", Protocol: "tcp", TargetIP: "127.0.0.1", TargetPort: targetPort, P2PKey: "test-direct-key", RendezvousAddress: udp.LocalAddr().String()}
	policy, err := relay.NewPolicy(relay.Config{AllowLoopbackPorts: []int{targetPort}})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTunnelManager("provider", policy, log)
	defer manager.Close()
	// Start the actual UDP provider directly: this test cannot silently pass via TCP.
	service, err := manager.startNATProvider(providerSession, f)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	go func() { <-providerSession.CloseChan(); service.Close() }()
	visitorForward := f
	visitorForward.Visitor = true
	dialCtx, dialCancel := context.WithTimeout(ctx, 2*time.Second)
	defer dialCancel()
	conn, err := manager.dialNAT(dialCtx, visitorSession, visitorForward)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	payload := bytes.Repeat([]byte("agent NAT traffic\n"), 4096)
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("corrupted direct payload")
	}
	revoked.Store(true)
	providerSession.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err = conn.Read(b[:]); err == nil {
		t.Fatal("revoked direct stream still open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("direct stream did not close on revocation")
	}
	retryCtx, retryCancel := context.WithTimeout(ctx, time.Second)
	defer retryCancel()
	if _, err = requestNAT(retryCtx, visitorSession, visitorForward, ""); err == nil {
		t.Fatal("revoked provider remains discoverable")
	}
}
