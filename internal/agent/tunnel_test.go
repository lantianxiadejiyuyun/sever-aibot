package agent

import "testing"

func TestTunnelWSURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "wss://node.example.com", want: "wss://node.example.com/tunnel", ok: true},
		{raw: "wss://node.example.com:443/tunnel", want: "wss://node.example.com:443/tunnel", ok: true},
		{raw: "WSS://Node.Example.com/custom", want: "wss://Node.Example.com/custom", ok: true},
		{raw: "https://node.example.com", want: "wss://node.example.com/tunnel", ok: true},
		{raw: "ws://127.0.0.1:7000", want: "ws://127.0.0.1:7000/tunnel", ok: true},
		{raw: "http://node.example.com/tunnel", want: "ws://node.example.com/tunnel", ok: true},
		{raw: "node.example.com", ok: false},
		{raw: "node.example.com:7000", ok: false},
		{raw: "wss://", ok: false},
		{raw: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := tunnelWSURL(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("tunnelWSURL(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNodeDialAddr(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		tls      bool
		want     string
		wantHost string
		wantErr  bool
	}{
		{name: "host and port", raw: "node.example.com:7000", tls: true, want: "node.example.com:7000", wantHost: "node.example.com"},
		{name: "bare host tls", raw: "node.example.com", tls: true, want: "node.example.com:443", wantHost: "node.example.com"},
		{name: "bare host no tls", raw: "node.example.com", tls: false, wantErr: true},
		{name: "ipv4 and port", raw: "127.0.0.1:7000", tls: false, want: "127.0.0.1:7000", wantHost: "127.0.0.1"},
		{name: "ipv6 bracket no port", raw: "[2001:db8::1]", tls: true, want: "[2001:db8::1]:443", wantHost: "2001:db8::1"},
		{name: "ipv6 with port", raw: "[2001:db8::1]:7000", tls: true, want: "[2001:db8::1]:7000", wantHost: "2001:db8::1"},
		{name: "empty", raw: "", tls: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, host, err := nodeDialAddr(tc.raw, tc.tls)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q %q", addr, host)
				}
				return
			}
			if err != nil {
				t.Fatalf("nodeDialAddr: %v", err)
			}
			if addr != tc.want || host != tc.wantHost {
				t.Fatalf("got %q %q, want %q %q", addr, host, tc.want, tc.wantHost)
			}
		})
	}
}
