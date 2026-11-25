//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"server-aibot/internal/agent"
	"time"

	"github.com/coder/websocket"
	"server-aibot/internal/model"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := newManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.startup = func(bool) (func() error, error) { return func() error { return nil }, nil }
	t.Cleanup(m.Close)
	return m
}
func testConfig() Config {
	return Config{ControlURL: "https://example.test", DeviceID: "dev_test", DeviceKey: "test-device-secret-123456", AutoConnect: true}
}

func TestEncryptedConfigRoundTripAndTamper(t *testing.T) {
	m := testManager(t)
	c := testConfig()
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), c.DeviceKey) || strings.Contains(string(data), `"deviceKey"`) {
		t.Fatal("plaintext device key saved")
	}
	loaded, err := m.Load()
	if err != nil || loaded != c {
		t.Fatalf("roundtrip mismatch: %v", err)
	}
	var s savedConfig
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	s.ProtectedKey[len(s.ProtectedKey)/2] ^= 0xff
	data, _ = json.Marshal(s)
	if err = os.WriteFile(filepath.Join(m.dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Load(); err == nil {
		t.Fatal("corrupted ciphertext accepted")
	}
}

func TestInvalidConfigurationDoesNotChangeStartup(t *testing.T) {
	m := testManager(t)
	called := false
	m.startup = func(bool) (func() error, error) { called = true; return nil, errors.New("unexpected") }
	for _, edit := range []func(*Config){
		func(c *Config) { c.ControlURL = "https://user:secret@example.test" },
		func(c *Config) { c.ControlURL = "file:///tmp/test" },
		func(c *Config) { c.ControlURL = "https://example.test?token=secret" },
		func(c *Config) { c.DeviceKey = "" },
		func(c *Config) { c.AllowCIDRs = "bad-range" },
		func(c *Config) { c.LoopbackPorts = "0,65536" },
	} {
		c := testConfig()
		edit(&c)
		if err := m.Save(c); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	if called {
		t.Fatal("invalid config changed startup")
	}
}

func TestWildcardDestinationPolicy(t *testing.T) {
	c := testConfig()
	c.AllowCIDRs, c.LoopbackPorts = "*", "*"
	p, err := validate(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"127.0.0.1", "::1", "8.8.8.8", "2001:4860:4860::8888", "169.254.1.1", "fe80::1"} {
		for _, port := range []int{1, 6768, 65535} {
			if err := p.Check(context.Background(), host, port); err != nil {
				t.Fatalf("%s:%d: %v", host, port, err)
			}
		}
	}
	for _, host := range []string{"169.254.169.254", "fd00:ec2::254", "0.0.0.0", "224.0.0.1"} {
		if p.Check(context.Background(), host, 80) == nil {
			t.Fatalf("special address accepted: %s", host)
		}
	}
	c.AllowCIDRs, c.LoopbackPorts = "", ""
	p, err = validate(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Check(context.Background(), "127.0.0.1", 6768) == nil {
		t.Fatal("default unexpectedly allows loopback")
	}
}

func TestSaveRestoresStartupOnDiskFailure(t *testing.T) {
	m := testManager(t)
	undone := false
	m.startup = func(bool) (func() error, error) { return func() error { undone = true; return nil }, nil }
	if err := os.Mkdir(filepath.Join(m.dir, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(testConfig()); err == nil {
		t.Fatal("expected failed replacement")
	}
	if !undone {
		t.Fatal("startup setting not restored")
	}
}

func TestAgentConnectAndStop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-device-secret-123456" {
			w.WriteHeader(401)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame model.AgentFrame
			if json.Unmarshal(data, &frame) != nil {
				return
			}
			if frame.Type == "hello" {
				body, _ := json.Marshal(model.AgentFrame{Type: "hello_ack", OK: true})
				if conn.Write(ctx, websocket.MessageText, body) != nil {
					return
				}
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	m := testManager(t)
	c := testConfig()
	c.ControlURL = server.URL
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !m.Status().Connected && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !m.Status().Connected {
		t.Fatal("Agent did not acknowledge connection: ", m.Logs())
	}
	if err := m.Save(c); err == nil {
		t.Fatal("changed running config")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	m.Stop()
	if m.Status().Running || m.Status().Connected {
		t.Fatal("Agent still running after stop")
	}
	if strings.Contains(m.Logs(), c.DeviceKey) {
		t.Fatal("secret in log")
	}
}

func TestReconnectStatus(t *testing.T) {
	now := time.Now()
	waiting := connectionStatus(agent.Snapshot{RetryAttempt: 3, NextRetryAt: now.Add(1500 * time.Millisecond), LastError: "secret"}, now)
	if waiting.Detail != "2 秒后自动重连（第 3 次）" || !waiting.Running || waiting.Connected {
		t.Fatalf("waiting status: %+v", waiting)
	}
	dialing := connectionStatus(agent.Snapshot{RetryAttempt: 3}, now)
	if dialing.Detail != "正在自动重连（第 3 次）" {
		t.Fatalf("dialing status: %+v", dialing)
	}
	unacked := connectionStatus(agent.Snapshot{Connected: true}, now)
	if unacked.Connected || unacked.Detail != "正在等待控制面确认…" {
		t.Fatalf("unacknowledged socket status: %+v", unacked)
	}
}

func TestManagerReconnectAfterOutageAndStopRetry(t *testing.T) {
	var offline atomic.Bool
	var attempts atomic.Int32
	connections := make(chan *websocket.Conn, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if offline.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connections <- conn
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame model.AgentFrame
			if json.Unmarshal(data, &frame) != nil {
				return
			}
			if frame.Type == "hello" {
				if conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"hello_ack","ok":true}`)) != nil {
					return
				}
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	m := testManager(t)
	c := testConfig()
	c.ControlURL = server.URL
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func(what string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s: %+v\n%s", what, m.Status(), m.Logs())
	}
	wait("initial hello acknowledgement", func() bool { return m.Status().Connected })
	first := <-connections
	offline.Store(true)
	first.CloseNow()
	wait("failed retry during outage", func() bool { return attempts.Load() >= 2 && !m.Status().NextRetryAt.IsZero() })
	if s := m.Status(); !s.Running || s.Connected || s.RetryAttempt < 2 {
		t.Fatalf("outage status: %+v", s)
	}
	offline.Store(false)
	wait("recovered hello acknowledgement", func() bool { return m.Status().Connected })
	second := <-connections
	m.mu.Lock()
	snapshot := m.client.State()
	m.mu.Unlock()
	if snapshot.RetryAttempt != 0 || !snapshot.NextRetryAt.IsZero() || snapshot.LastError != "" {
		t.Fatalf("stale retry state after recovery: %+v", snapshot)
	}
	offline.Store(true)
	second.CloseNow()
	wait("pending retry after new drop", func() bool { return !m.Status().NextRetryAt.IsZero() })
	if s := m.Status(); s.RetryAttempt != 1 {
		t.Fatalf("backoff did not reset: %+v", s)
	}
	retryAt := m.Status().NextRetryAt
	m.Stop()
	count := attempts.Load()
	offline.Store(false)
	// Wait past the cancelled retry deadline: a restored server must not restart a stopped client.
	time.Sleep(max(0, time.Until(retryAt)) + 200*time.Millisecond)
	if s := m.Status(); s.Running || s.Connected || s.RetryAttempt != 0 || !s.NextRetryAt.IsZero() {
		t.Fatalf("stopped status: %+v", s)
	}
	if attempts.Load() != count {
		t.Fatal("manual stop allowed another dial")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	wait("explicit restart", func() bool { return m.Status().Connected })
	m.Stop()
	if m.Status().Running {
		t.Fatal("stop while connected failed")
	}
}

func TestLogRedactionAndBound(t *testing.T) {
	b := &logBuffer{}
	b.setSecret("secret-value")
	b.Write([]byte("token=secret-value\n"))
	if strings.Contains(b.String(), "secret-value") {
		t.Fatal("secret retained")
	}
	b.Write([]byte(strings.Repeat("line\n", 30000)))
	if len(b.String()) > 64<<10 {
		t.Fatal("unbounded log")
	}
}
