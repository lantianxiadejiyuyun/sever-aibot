package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"server-aibot/internal/model"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func marshalFrame(f model.AgentFrame) []byte {
	b, _ := json.Marshal(f)
	return b
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// These fixtures implement only the control socket, not the access transport.
func controlOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/ws" {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func TestRunHandshake(t *testing.T) {
	hellos := make(chan model.AgentFrame, 4)
	beats := make(chan struct{}, 16)
	headers := make(chan [3]string, 4)

	srv := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) {
		select {
		case headers <- [3]string{
			r.Header.Get("X-Device-ID"),
			r.Header.Get("Authorization"),
			r.Header.Get("User-Agent"),
		}:
		default:
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var f model.AgentFrame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			switch f.Type {
			case "hello":
				select {
				case hellos <- f:
				default:
				}
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true}))
			case "heartbeat":
				if f.Metrics == nil || f.Metrics.CollectedAt.IsZero() || len(f.Metrics.Status) != 6 {
					t.Error("heartbeat missing metric sample")
				}
				select {
				case beats <- struct{}{}:
				default:
				}
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "heartbeat_ack", TS: time.Now().Unix(), OK: true}))
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		ControlURL:        srv.URL,
		DeviceID:          "dev-1",
		DeviceKey:         "secret-key-value",
		HeartbeatInterval: 20 * time.Millisecond,
		ReadTimeout:       2 * time.Second,
		ConnectTimeout:    time.Second,
		ReconnectBase:     5 * time.Millisecond,
		ReconnectMax:      20 * time.Millisecond,
	}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case hello := <-hellos:
		if hello.Version != Version {
			t.Errorf("hello version = %q, want %q", hello.Version, Version)
		}
		if hello.OS != runtime.GOOS {
			t.Errorf("hello os = %q, want %q", hello.OS, runtime.GOOS)
		}
		if hello.Arch != runtime.GOARCH {
			t.Errorf("hello arch = %q, want %q", hello.Arch, runtime.GOARCH)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for hello")
	}

	select {
	case h := <-headers:
		if h[0] != "dev-1" {
			t.Errorf("X-Device-ID = %q, want dev-1", h[0])
		}
		if h[1] != "Bearer secret-key-value" {
			t.Errorf("Authorization header was not the bearer device key")
		}
		if !strings.Contains(h[2], "server-aibot-agent") {
			t.Errorf("User-Agent = %q, want server-aibot-agent", h[2])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handshake headers")
	}

	select {
	case <-beats:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for heartbeat")
	}

	snap := c.State()
	if !snap.Connected {
		t.Error("State.Connected = false while running")
	}
	if snap.LastHelloAckAt.IsZero() {
		t.Error("State.LastHelloAckAt is zero after hello_ack")
	}
	if snap.Reconnects != 0 {
		t.Errorf("State.Reconnects = %d, want 0", snap.Reconnects)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if c.State().Connected {
		t.Error("State.Connected = true after shutdown")
	}
}

func TestRunReconnectsAfterDrop(t *testing.T) {
	var conns atomic.Int32
	secondAck := make(chan struct{}, 1)

	srv := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) {
		n := conns.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var f model.AgentFrame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			switch f.Type {
			case "hello":
				if n == 1 {
					// Drop the first connection without acknowledging hello.
					return
				}
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true}))
				select {
				case secondAck <- struct{}{}:
				default:
				}
			case "heartbeat":
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "heartbeat_ack", TS: time.Now().Unix(), OK: true}))
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		ControlURL:        srv.URL,
		DeviceID:          "dev-2",
		DeviceKey:         "secret",
		HeartbeatInterval: 20 * time.Millisecond,
		ReadTimeout:       2 * time.Second,
		ConnectTimeout:    time.Second,
		ReconnectBase:     5 * time.Millisecond,
		ReconnectMax:      20 * time.Millisecond,
	}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case <-secondAck:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not reconnect and complete a hello handshake")
	}
	if n := conns.Load(); n < 2 {
		t.Fatalf("connections = %d, want at least 2", n)
	}
	if r := c.State().Reconnects; r < 1 {
		t.Fatalf("State.Reconnects = %d, want at least 1", r)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunUnacknowledgedSessionRetriesAndCancels(t *testing.T) {
	var connections atomic.Int32
	srv := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connections.Add(1)
		// Accept and read hello/heartbeats, but never acknowledge anything.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	c := newTestClient(t, Config{ControlURL: srv.URL, DeviceID: "unacked", DeviceKey: "secret", ReadTimeout: 50 * time.Millisecond, ReconnectBase: 200 * time.Millisecond, ReconnectMax: time.Second}, quietLogger())
	c.jitter = func(d time.Duration) time.Duration { return d }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	defer cancel()
	go func() { done <- c.Run(ctx) }()
	waitFor(t, 5*time.Second, "second unacknowledged timeout", func() bool { return c.State().RetryAttempt == 2 && !c.State().NextRetryAt.IsZero() })
	s := c.State()
	if connections.Load() != 2 || !s.LastHelloAckAt.IsZero() || s.Connected {
		t.Fatalf("unexpected unacknowledged state: %+v", s)
	}
	if remaining := time.Until(s.NextRetryAt); remaining < 200*time.Millisecond || remaining > 400*time.Millisecond {
		t.Fatalf("backoff not doubled: %v", remaining)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt backoff")
	}
	if s := c.State(); s.Connected || s.RetryAttempt != 0 || !s.NextRetryAt.IsZero() {
		t.Fatalf("stale stopped state: %+v", s)
	}
	time.Sleep(450 * time.Millisecond)
	if connections.Load() != 2 {
		t.Fatal("retried after cancellation")
	}
}

func TestRunLivenessTimeoutReconnects(t *testing.T) {
	var conns atomic.Int32
	firstAck := make(chan struct{}, 1)

	srv := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var f model.AgentFrame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			if f.Type == "hello" {
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true}))
				select {
				case firstAck <- struct{}{}:
				default:
				}
			}
			// Heartbeats are intentionally ignored: no control frames are
			// sent after hello_ack, so the client watchdog must fire.
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		ControlURL:        srv.URL,
		DeviceID:          "dev-3",
		DeviceKey:         "secret",
		HeartbeatInterval: 10 * time.Millisecond,
		ReadTimeout:       120 * time.Millisecond,
		ConnectTimeout:    time.Second,
		ReconnectBase:     5 * time.Millisecond,
		ReconnectMax:      20 * time.Millisecond,
	}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case <-firstAck:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first hello_ack")
	}
	waitFor(t, 3*time.Second, "liveness-driven reconnect", func() bool {
		return conns.Load() >= 2 && c.State().Reconnects >= 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestDeviceKeyNeverLogged(t *testing.T) {
	const secret = "test-device-key-value"
	var buf lockedBuffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	beats := make(chan struct{}, 1)
	srv := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var f model.AgentFrame
			if json.Unmarshal(data, &f) != nil {
				continue
			}
			switch f.Type {
			case "hello":
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true}))
			case "heartbeat":
				if f.Metrics == nil || f.Metrics.CollectedAt.IsZero() || len(f.Metrics.Status) != 6 {
					t.Error("heartbeat missing metric sample")
				}
				select {
				case beats <- struct{}{}:
				default:
				}
				_ = conn.Write(r.Context(), websocket.MessageText,
					marshalFrame(model.AgentFrame{Type: "heartbeat_ack", OK: true}))
			}
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		ControlURL:        "http://user:url-password@" + strings.TrimPrefix(srv.URL, "http://"),
		DeviceID:          "dev-4",
		DeviceKey:         secret,
		HeartbeatInterval: 20 * time.Millisecond,
		ReadTimeout:       2 * time.Second,
		ConnectTimeout:    time.Second,
	}, log)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case <-beats:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for heartbeat")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}

	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatal("device key appeared in agent logs")
	}
	if strings.Contains(out, "Bearer "+secret) {
		t.Fatal("Authorization header appeared in agent logs")
	}
	if strings.Contains(out, "url-password") {
		t.Fatal("URL userinfo appeared in agent logs")
	}
}

func TestEnsureStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := EnsureStateDir(dir); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("state path is not a directory")
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("state dir perm = %o, want 700", perm)
		}
	}
	// Idempotent on an existing directory.
	if err := EnsureStateDir(dir); err != nil {
		t.Fatalf("EnsureStateDir second call: %v", err)
	}
	if err := EnsureStateDir(""); err == nil {
		t.Fatal("EnsureStateDir(\"\") = nil, want error")
	}
}

func newTestClient(t *testing.T, cfg Config, log *slog.Logger) *Client {
	t.Helper()
	c, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
