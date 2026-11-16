package edge

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

// clientHelloBytes captures the first records a real TLS client sends.
func clientHelloBytes(t *testing.T, cfg *tls.Config) []byte {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_ = tls.Client(clientConn, cfg).HandshakeContext(ctx)
		_ = clientConn.Close()
	}()

	host, prefix, err := ReadClientHello(serverConn)
	if err != nil {
		t.Fatalf("ReadClientHello: %v", err)
	}
	if host != normalizeHost(cfg.ServerName) {
		t.Fatalf("host = %q, want %q", host, cfg.ServerName)
	}
	return prefix
}

func TestReadClientHelloSNI(t *testing.T) {
	prefix := clientHelloBytes(t, &tls.Config{ServerName: "node.dragnoble.top", InsecureSkipVerify: true})
	if len(prefix) == 0 || prefix[0] != recordTypeHandshake {
		t.Fatalf("prefix does not start with a handshake record: %v", prefix)
	}
}

func TestReadClientHelloNoSNI(t *testing.T) {
	// Dialing an IP address omits the server_name extension.
	prefix := clientHelloBytes(t, &tls.Config{InsecureSkipVerify: true})
	if len(prefix) == 0 {
		t.Fatal("empty prefix")
	}
}

func TestReadClientHelloRejectsPlaintext(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	go func() {
		_, _ = clientConn.Write([]byte("GET / HTTP/1.1\r\nHost: aibot.dragnoble.top\r\n\r\n"))
	}()
	_, _, err := ReadClientHello(serverConn)
	if err == nil || !strings.Contains(err.Error(), "TLS handshake") {
		t.Fatalf("err = %v, want not-a-TLS-handshake", err)
	}
}

func TestParseRoutes(t *testing.T) {
	routes, err := ParseRoutes("AIBOT.dragnoble.top=127.0.0.1:7400, node.dragnoble.top=127.0.0.1:7000")
	if err != nil {
		t.Fatalf("ParseRoutes: %v", err)
	}
	if routes["aibot.dragnoble.top"] != "127.0.0.1:7400" {
		t.Fatalf("routes = %v", routes)
	}
	if routes["node.dragnoble.top"] != "127.0.0.1:7000" {
		t.Fatalf("routes = %v", routes)
	}
	if _, err := ParseRoutes("a.example.com"); err == nil {
		t.Fatal("expected error for missing upstream")
	}
	if _, err := ParseRoutes("a.example.com=127.0.0.1:1,a.example.com=127.0.0.1:2"); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestRouteDefault(t *testing.T) {
	s, err := New(Config{
		Listen:          "127.0.0.1:0",
		Routes:          map[string]string{"aibot.dragnoble.top": "127.0.0.1:7400"},
		DefaultUpstream: "127.0.0.1:7400",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if up, ok := s.Route("AIBOT.dragnoble.top."); !ok || up != "127.0.0.1:7400" {
		t.Fatalf("route = %q %v", up, ok)
	}
	if up, ok := s.Route("unknown.example.com"); !ok || up != "127.0.0.1:7400" {
		t.Fatalf("default route = %q %v", up, ok)
	}

	strict, err := New(Config{Listen: "127.0.0.1:0", Routes: map[string]string{"aibot.dragnoble.top": "127.0.0.1:7400"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := strict.Route("unknown.example.com"); ok {
		t.Fatal("expected unknown name to be refused without a default")
	}
}
