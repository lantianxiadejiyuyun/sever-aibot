package node_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"server-aibot/internal/agent"
	"server-aibot/internal/api"
	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/model"
	"server-aibot/internal/node"
	"server-aibot/internal/relay"
	"server-aibot/internal/store"
)

// TestRelayEndToEnd exercises the full node-relay path: control plane config,
// node listener, agent tunnel, and target dial. Traffic flows public → node →
// agent → loopback target.
func TestRelayEndToEnd(t *testing.T) {
	runRelayEndToEnd(t, false)
}

// TestRelayEndToEndWebSocket covers the proxied deployment: the tunnel is a
// WebSocket on the node side and the agent dials a ws:// address.
func TestRelayEndToEndWebSocket(t *testing.T) {
	runRelayEndToEnd(t, true)
}

func runRelayEndToEnd(t *testing.T, httpTunnel bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := store.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.EnsureAdmin(ctx, "admin", "relay-test-admin-pass"); err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	h := hub.New(st, log)
	srv := httptest.NewServer(api.New(config.Control{MasterKey: master}, st, h, log).Routes())
	defer srv.Close()

	target := startEcho(t)
	targetPort := target.Addr().(*net.TCPAddr).Port
	tunnelPort := freePort(t)
	entryPort := freePort(t)

	nodeAddr := fmt.Sprintf("127.0.0.1:%d", tunnelPort)
	if httpTunnel {
		nodeAddr = fmt.Sprintf("ws://127.0.0.1:%d", tunnelPort)
	}
	n, nodeKey, err := st.CreateNode(ctx, model.Node{
		Name: "edge-1", Addr: nodeAddr, Protocol: "tcp",
		Features: model.NodeFeatures{Forward: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, deviceKey, err := st.CreateDevice(ctx, "lan-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.CreateForward(ctx, model.Forward{
		Name: "echo", Kind: "port", DeviceID: d.ID, NodeID: n.ID,
		TargetIP: "127.0.0.1", TargetPort: targetPort, RemotePort: entryPort, State: "applying",
	})
	if err != nil {
		t.Fatal(err)
	}

	policy, err := relay.NewPolicy(relay.Config{AllowLoopbackPorts: []int{targetPort}})
	if err != nil {
		t.Fatal(err)
	}
	ac, err := agent.New(agent.Config{
		ControlURL: srv.URL, DeviceID: d.ID, DeviceKey: deviceKey, Relay: policy,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	nr, err := node.New(node.Config{
		ControlURL: srv.URL, NodeID: n.ID, NodeKey: nodeKey,
		TunnelListen: fmt.Sprintf("127.0.0.1:%d", tunnelPort), EntryHost: "127.0.0.1",
		TunnelHTTP: httpTunnel,
	}, log)
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = nr.Run(ctx) }()
	go func() { _ = ac.Run(ctx) }()
	defer cancel()

	waitFor(t, 10*time.Second, func() bool {
		got, err := st.GetForward(context.Background(), f.ID)
		return err == nil && got.State == "applied"
	}, "forward did not reach applied state")

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	var conn net.Conn
	waitFor(t, 10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", entry, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			c.Close()
			return false
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			c.Close()
			return false
		}
		conn = c
		return true
	}, "relay did not return the echo response")
	if conn != nil {
		_ = conn.Close()
	}
}

func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(message)
}
