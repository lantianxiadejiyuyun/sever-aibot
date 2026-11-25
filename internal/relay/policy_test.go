package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

type stubResolver struct {
	addrs map[string][]netip.Addr
	err   error
}

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if s.err != nil {
		return nil, s.err
	}
	if addrs, ok := s.addrs[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func mustPolicy(t *testing.T, cfg Config) *Policy {
	t.Helper()
	p, err := NewPolicy(cfg)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

func TestNewPolicyValidation(t *testing.T) {
	if _, err := NewPolicy(Config{AllowCIDRs: []string{"not-a-cidr"}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad CIDR err = %v, want ErrInvalidConfig", err)
	}
	if _, err := NewPolicy(Config{AllowLoopbackPorts: []int{0}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad loopback port err = %v, want ErrInvalidConfig", err)
	}
	if _, err := NewPolicy(Config{AllowLoopbackPorts: []int{65536}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad loopback port err = %v, want ErrInvalidConfig", err)
	}
	p := mustPolicy(t, Config{})
	if p.timeout != defaultDialTimeout {
		t.Fatalf("default timeout = %v, want %v", p.timeout, defaultDialTimeout)
	}
}

func TestCheckDefaultPolicy(t *testing.T) {
	p := mustPolicy(t, Config{})

	allowed := []struct {
		host string
		port int
	}{
		{"10.1.2.3", 443},
		{"10.255.255.255", 22},
		{"172.16.0.1", 80},
		{"172.31.255.255", 8080},
		{"192.168.0.1", 1},
		{"192.168.255.254", 65535},
		{"fd12:3456:789a::1", 443},
		{"fc00::1", 8080},
		{"::ffff:10.0.0.5", 80}, // IPv4-mapped private address
	}
	for _, tc := range allowed {
		if err := p.Check(context.Background(), tc.host, tc.port); err != nil {
			t.Errorf("Check(%s:%d) = %v, want allowed", tc.host, tc.port, err)
		}
	}

	denied := []struct {
		host string
		port int
	}{
		{"8.8.8.8", 53},
		{"1.1.1.1", 443},
		{"172.32.0.1", 80},
		{"2001:4860:4860::8888", 443},
		{"169.254.169.254", 80},
		{"::ffff:169.254.169.254", 80},
		{"fd00:ec2::254", 80},
		{"169.254.1.1", 22},
		{"fe80::1", 22},
		{"0.0.0.0", 80},
		{"::", 80},
		{"224.0.0.1", 80},
		{"ff02::1", 80},
		{"255.255.255.255", 80},
		{"127.0.0.1", 80},
		{"127.0.0.1", 22},
		{"::1", 443},
	}
	for _, tc := range denied {
		err := p.Check(context.Background(), tc.host, tc.port)
		if err == nil {
			t.Errorf("Check(%s:%d) = nil, want denied", tc.host, tc.port)
			continue
		}
		if !errors.Is(err, ErrDenied) {
			t.Errorf("Check(%s:%d) = %v, want ErrDenied", tc.host, tc.port, err)
		}
	}
}

func TestLoopbackRequiresAllowlistedPort(t *testing.T) {
	p := mustPolicy(t, Config{AllowLoopbackPorts: []int{22, 5432}})
	for _, host := range []string{"127.0.0.1", "::1", "[::1]"} {
		if err := p.Check(context.Background(), host, 22); err != nil {
			t.Errorf("Check(%s:22) = %v, want allowed", host, err)
		}
		if err := p.Check(context.Background(), host, 5432); err != nil {
			t.Errorf("Check(%s:5432) = %v, want allowed", host, err)
		}
		if err := p.Check(context.Background(), host, 80); !errors.Is(err, ErrDenied) {
			t.Errorf("Check(%s:80) = %v, want ErrDenied", host, err)
		}
	}
}

func TestAllowCIDRsAreAdditive(t *testing.T) {
	p := mustPolicy(t, Config{AllowCIDRs: []string{"203.0.113.0/24", "2001:db8::/32", "198.51.100.7"}})
	if err := p.Check(context.Background(), "203.0.113.9", 443); err != nil {
		t.Errorf("documentation range denied: %v", err)
	}
	if err := p.Check(context.Background(), "2001:db8::1", 443); err != nil {
		t.Errorf("documentation v6 range denied: %v", err)
	}
	if err := p.Check(context.Background(), "198.51.100.7", 443); err != nil {
		t.Errorf("bare allowed address denied: %v", err)
	}
	// Defaults remain active.
	if err := p.Check(context.Background(), "10.0.0.5", 443); err != nil {
		t.Errorf("private default denied: %v", err)
	}
	// Public addresses outside the configured ranges stay denied.
	if err := p.Check(context.Background(), "8.8.8.8", 443); !errors.Is(err, ErrDenied) {
		t.Errorf("public address = %v, want ErrDenied", err)
	}
	// A loopback CIDR does not bypass the port gate.
	p2 := mustPolicy(t, Config{AllowCIDRs: []string{"127.0.0.0/8"}})
	if err := p2.Check(context.Background(), "127.0.0.1", 80); !errors.Is(err, ErrDenied) {
		t.Errorf("loopback via CIDR = %v, want ErrDenied", err)
	}
}

func TestLinkLocal(t *testing.T) {
	strict := mustPolicy(t, Config{})
	if err := strict.Check(context.Background(), "169.254.10.10", 22); !errors.Is(err, ErrDenied) {
		t.Errorf("link-local default = %v, want ErrDenied", err)
	}

	relaxed := mustPolicy(t, Config{AllowLinkLocal: true})
	if err := relaxed.Check(context.Background(), "169.254.10.10", 22); err != nil {
		t.Errorf("link-local with AllowLinkLocal = %v, want allowed", err)
	}
	if err := relaxed.Check(context.Background(), "fe80::1", 22); err != nil {
		t.Errorf("v6 link-local with AllowLinkLocal = %v, want allowed", err)
	}
	// Metadata is still denied even when link-local is enabled.
	if err := relaxed.Check(context.Background(), "169.254.169.254", 80); !errors.Is(err, ErrDenied) {
		t.Errorf("metadata with AllowLinkLocal = %v, want ErrDenied", err)
	}
}

func TestHostnameResolution(t *testing.T) {
	resolver := stubResolver{addrs: map[string][]netip.Addr{
		"cache.internal":        {netip.MustParseAddr("10.0.0.5")},
		"metadata.evil.test":    {netip.MustParseAddr("169.254.169.254")},
		"fd-metadata.evil.test": {netip.MustParseAddr("fd00:ec2::254")},
		"mixed.evil.test":       {netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("8.8.8.8")},
	}}
	p, err := NewPolicyWithResolver(Config{}, resolver)
	if err != nil {
		t.Fatalf("NewPolicyWithResolver: %v", err)
	}
	ctx := context.Background()
	if err := p.Check(ctx, "cache.internal", 6379); err != nil {
		t.Errorf("private hostname denied: %v", err)
	}
	for _, host := range []string{"metadata.evil.test", "fd-metadata.evil.test", "mixed.evil.test", "missing.internal"} {
		if err := p.Check(ctx, host, 80); !errors.Is(err, ErrDenied) {
			t.Errorf("Check(%s) = %v, want ErrDenied", host, err)
		}
	}
}

func TestPortValidation(t *testing.T) {
	p := mustPolicy(t, Config{})
	for _, port := range []int{-1, 0, 65536, 100000} {
		err := p.Check(context.Background(), "10.0.0.1", port)
		if !errors.Is(err, ErrInvalidPort) {
			t.Errorf("Check(port=%d) = %v, want ErrInvalidPort", port, err)
		}
	}
}

func TestDialContextLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	p := mustPolicy(t, Config{AllowLoopbackPorts: []int{port}, Timeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := p.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}

	// Same address, but the port is no longer allowlisted.
	blocked := mustPolicy(t, Config{AllowLoopbackPorts: []int{22}})
	if _, err := blocked.DialContext(ctx, "tcp", ln.Addr().String()); !errors.Is(err, ErrDenied) {
		t.Fatalf("DialContext without allowlisted port = %v, want ErrDenied", err)
	}
}

func TestDialContextHostnameUsesVettedAddress(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	resolver := stubResolver{addrs: map[string][]netip.Addr{
		"service.internal": {netip.MustParseAddr("127.0.0.1")},
	}}
	p, err := NewPolicyWithResolver(Config{AllowLoopbackPorts: []int{port}}, resolver)
	if err != nil {
		t.Fatalf("NewPolicyWithResolver: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := p.DialContext(ctx, "tcp", net.JoinHostPort("service.internal", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("DialContext hostname: %v", err)
	}
	_ = conn.Close()
}

func TestDialContextRejectsBadInput(t *testing.T) {
	p := mustPolicy(t, Config{})
	ctx := context.Background()
	if _, err := p.DialContext(ctx, "sctp", "10.0.0.1:53"); !errors.Is(err, ErrDenied) {
		t.Errorf("sctp = %v, want ErrDenied", err)
	}
	if _, err := p.DialContext(ctx, "tcp", "10.0.0.1:notaport"); !errors.Is(err, ErrInvalidPort) {
		t.Errorf("non-numeric port = %v, want ErrInvalidPort", err)
	}
	if _, err := p.DialContext(ctx, "tcp", "10.0.0.1:70000"); !errors.Is(err, ErrInvalidPort) {
		t.Errorf("oversized port = %v, want ErrInvalidPort", err)
	}
	if _, err := p.DialContext(ctx, "tcp", "no-port"); !errors.Is(err, ErrDenied) {
		t.Errorf("missing port = %v, want ErrDenied", err)
	}
}
