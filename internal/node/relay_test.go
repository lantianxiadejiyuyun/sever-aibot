package node_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"server-aibot/internal/tunnel"
	"strings"
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
	runRelayEndToEnd(t, false, "tcp")
}

// TestRelayEndToEndWebSocket covers the proxied deployment: the tunnel is a
// WebSocket on the node side and the agent dials a ws:// address.
func TestRelayEndToEndWebSocket(t *testing.T) {
	runRelayEndToEnd(t, true, "tcp")
}

func TestRelayP2PRuleForbidFallback(t *testing.T)    { runRelayEndToEnd(t, false, "p2p-rule-no-relay") }
func TestRelayP2PFailedTarget(t *testing.T)          { runRelayEndToEnd(t, false, "p2p-failed-target") }
func TestRelayP2PExplicitAllowFallback(t *testing.T) { runRelayEndToEnd(t, false, "p2p-allow-relay") }
func TestRelayP2PDirect(t *testing.T)                { runRelayEndToEnd(t, false, "p2p-direct") }
func TestRelayP2PForbidFallback(t *testing.T)        { runRelayEndToEnd(t, false, "p2p-no-relay") }
func TestRelayProxy(t *testing.T)                    { runRelayEndToEnd(t, false, "proxy") }
func TestRelayPrivate(t *testing.T)                  { runRelayEndToEnd(t, false, "tunnel") }
func TestRelayP2PRelayFallback(t *testing.T)         { runRelayEndToEnd(t, false, "p2p") }
func TestRelayUDP(t *testing.T)                      { runRelayEndToEnd(t, false, "udp") }
func TestRelayHTTPHost(t *testing.T)                 { runRelayEndToEnd(t, false, "host") }
func runRelayEndToEnd(t *testing.T, httpTunnel bool, mode string) {
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
	srv := httptest.NewServer(api.New(config.Control{MasterKey: master, SessionTTL: 12}, st, h, log).Routes())
	defer srv.Close()

	targetPort := 0
	if mode == "udp" {
		ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		targetPort = ln.LocalAddr().(*net.UDPAddr).Port
		go func() {
			buf := make([]byte, 65535)
			for {
				n, addr, err := ln.ReadFromUDP(buf)
				if err != nil {
					return
				}
				_, _ = ln.WriteToUDP(buf[:n], addr)
			}
		}()
	} else if mode == "host" {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "host-response:"+r.Host) }))
		defer target.Close()
		targetPort = target.Listener.Addr().(*net.TCPAddr).Port
	} else {
		target := startEcho(t)
		targetPort = target.Addr().(*net.TCPAddr).Port
	}
	if mode == "p2p-failed-target" {
		targetPort = freePort(t)
	}
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	directPort := freePort(t)
	directAddress := fmt.Sprintf("127.0.0.1:%d", directPort)
	if strings.HasPrefix(mode, "p2p") && mode != "p2p-direct" {
		directAddress = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}

	nodeAddr := fmt.Sprintf("127.0.0.1:%d", tunnelPort)
	if httpTunnel {
		nodeAddr = fmt.Sprintf("ws://127.0.0.1:%d", tunnelPort)
	}
	n, nodeKey, err := st.CreateNode(ctx, model.Node{
		Name: "edge-1", Addr: nodeAddr, Protocol: "tcp", P2PDisableForward: mode == "p2p-direct" || mode == "p2p-no-relay",
		Features: model.NodeFeatures{Forward: true, Web: true, Tunnel: true, P2P: true, Proxy: true},
		HTTPPort: func() int {
			if mode == "host" {
				return entryPort
			}
			return 0
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	d, deviceKey, err := st.CreateDevice(ctx, "lan-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	visitorID, visitorKey := "", ""
	if mode == "tunnel" || mode == "proxy" || strings.HasPrefix(mode, "p2p") {
		v, key, err := st.CreateDevice(ctx, "visitor", "", "")
		if err != nil {
			t.Fatal(err)
		}
		visitorID, visitorKey = v.ID, key
	}
	f, err := st.CreateForward(ctx, model.Forward{
		Name: "echo", Kind: func() string {
			if strings.HasPrefix(mode, "p2p") {
				return "p2p"
			}
			if mode == "host" || mode == "tunnel" || mode == "proxy" {
				return mode
			}
			return "port"
		}(), Domain: "test.example.com", Options: func() string {
			if mode == "udp" {
				return `{"protocol":"udp"}`
			}
			if strings.HasPrefix(mode, "p2p") {
				values := map[string]any{"directListen": fmt.Sprintf("127.0.0.1:%d", directPort), "directAddress": directAddress}
				if mode == "p2p-rule-no-relay" {
					values["relay"] = false
				}
				if mode == "p2p-allow-relay" || mode == "p2p-no-relay" {
					values["relay"] = true
				}
				options, _ := json.Marshal(values)
				return string(options)
			}
			return "{}"
		}(), DeviceID: d.ID, NodeID: n.ID, ClientID: visitorID,
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
	if visitorID != "" {
		visitor, err := agent.New(agent.Config{ControlURL: srv.URL, DeviceID: visitorID, DeviceKey: visitorKey, Relay: policy}, log)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = visitor.Run(ctx) }()
	}
	defer cancel()

	waitFor(t, 10*time.Second, func() bool {
		got, err := st.GetForward(context.Background(), f.ID)
		return err == nil && got.State == "applied"
	}, "forward did not reach applied state")

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	if mode == "host" {
		waitFor(t, 10*time.Second, func() bool {
			req, _ := http.NewRequest("GET", "http://"+entry+"/", nil)
			req.Host = "test.example.com"
			resp, err := (&http.Client{Timeout: time.Second}).Do(req)
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode == 200 && string(body) == "host-response:test.example.com"
		}, "host routing failed")
		req, _ := http.NewRequest("GET", "http://"+entry+"/", nil)
		req.Host = "unknown.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("unknown domain must not route")
		}
		return
	}
	if mode == "p2p-no-relay" || mode == "p2p-rule-no-relay" || mode == "p2p-failed-target" {
		c, err := net.DialTimeout("tcp", entry, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(7 * time.Second))
		_, _ = c.Write([]byte("no-relay"))
		if _, err := c.Read(make([]byte, 32)); err == nil {
			t.Fatal("disabled fallback unexpectedly relayed")
		}
		waitFor(t, 5*time.Second, func() bool { return st.ForwardPath(context.Background(), f.ID, f.Revision) == "unavailable" }, "failed direct path was not reported")
		return
	}
	network := "tcp"
	if mode == "udp" {
		network = "udp"
	}
	var conn net.Conn
	waitFor(t, 10*time.Second, func() bool {
		c, err := net.DialTimeout(network, entry, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if mode == "proxy" {
			fmt.Fprintf(c, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", targetPort, targetPort)
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil || resp.StatusCode != 200 {
				c.Close()
				return false
			}
		}
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
		if strings.HasPrefix(mode, "p2p") {
			expected := "relay"
			if mode == "p2p-direct" {
				expected = "direct"
			}
			waitFor(t, 5*time.Second, func() bool { return st.ForwardPath(context.Background(), f.ID, f.Revision) == expected }, "wrong P2P path report")
		}
		if mode == "udp" {
			payload := strings.Repeat("u", 60000)
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write([]byte(payload)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 65535)
			n, err := conn.Read(buf)
			if err != nil || string(buf[:n]) != payload {
				t.Fatalf("large UDP packet boundary: n=%d err=%v", n, err)
			}
			if _, err := conn.Write(nil); err != nil {
				t.Fatal(err)
			}
			n, err = conn.Read(buf)
			if err != nil || n != 0 {
				t.Fatalf("empty UDP packet: n=%d err=%v", n, err)
			}
		}
		waitFor(t, 5*time.Second, func() bool {
			got, _ := st.GetForward(context.Background(), f.ID)
			return got.State == "observed" && got.TrafficBytes >= 8
		}, "active connection was not observed")
		if mode == "tcp" || mode == "tunnel" || strings.HasPrefix(mode, "p2p") {
			client, csrf := relayLogin(t, srv.URL)
			if mode == "p2p-direct" {
				req, _ := http.NewRequest("POST", srv.URL+"/api/v1/devices/"+visitorID+"/rotate-key", nil)
				req.Header.Set("X-CSRF-Token", csrf)
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("rotate status %d", resp.StatusCode)
				}
				current, err := st.GetForward(context.Background(), f.ID)
				if err != nil || current.Revision <= f.Revision {
					t.Fatal("rotation did not invalidate forward revision")
				}
				keyFor := func(revision int64) string {
					mac := hmac.New(sha256.New, master)
					fmt.Fprintf(mac, "aibot-direct/v1/%s/%d", f.ID, revision)
					return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
				}
				authenticate := func(key string, revision int64) error {
					c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", directAddress, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
					if err != nil {
						return err
					}
					defer c.Close()
					c.SetDeadline(time.Now().Add(time.Second))
					if err = tunnel.ProveDirect(c, key, f.ID, revision); err != nil {
						return err
					}
					status, _, err := tunnel.ReadResult(c)
					if err == nil && status != tunnel.StatusOK {
						return fmt.Errorf("status %d", status)
					}
					return err
				}
				waitFor(t, 10*time.Second, func() bool { return authenticate(keyFor(current.Revision), current.Revision) == nil }, "provider did not accept fresh rotated credential")
				if err := authenticate(keyFor(f.Revision), f.Revision); err == nil {
					t.Fatal("old visitor direct credential survived key rotation")
				}
			}
			req, _ := http.NewRequest("POST", srv.URL+"/api/v1/forwards/"+f.ID+"/stop", nil)
			req.Header.Set("X-CSRF-Token", csrf)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("stop status %d", resp.StatusCode)
			}
			waitFor(t, 5*time.Second, func() bool { got, _ := st.GetForward(context.Background(), f.ID); return got.State == "stopped" }, "node did not confirm revocation")
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, _ = conn.Write([]byte("after-stop"))
			if _, err := conn.Read(make([]byte, 32)); err == nil {
				t.Fatal("existing business connection survived revocation")
			}
		}
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

func relayLogin(t *testing.T, base string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := client.Post(base+"/api/v1/auth/login", "application/json", bytes.NewBufferString(`{"username":"admin","password":"relay-test-admin-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return client, body.CSRF
}
