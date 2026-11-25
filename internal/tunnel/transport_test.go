package tunnel

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestUDPAddress(t *testing.T) {
	for _, raw := range []string{"127.0.0.1:7000", "node.example", "wss://node.example/tunnel"} {
		transport, _, _, err := UDPAddress(raw)
		if err != nil || transport != "" {
			t.Fatalf("non-UDP %q: %q %v", raw, transport, err)
		}
	}
	for _, raw := range []string{"kcp://node.example:7000", "quic://[::1]:7000"} {
		transport, addr, host, err := UDPAddress(raw)
		if err != nil || transport == "" || addr == "" || host == "" {
			t.Fatalf("valid endpoint %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"kcp://node.example", "quic://u:p@node.example:7000", "quic://node.example:7000/path", "kcp://node.example:7000?insecure=1"} {
		if _, _, _, err := UDPAddress(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestUDPDialCancellation(t *testing.T) {
	for _, transport := range []string{"kcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			socket, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				c, err := DialUDP(ctx, transport, socket.LocalAddr().String(), &tls.Config{ServerName: "localhost"})
				if c != nil {
					c.Close()
				}
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled dial succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("dial ignored cancellation")
			}
			if ln, err := ListenUDP(transport, "127.0.0.1:0", nil); err == nil {
				ln.Close()
				t.Fatal("listener accepted missing TLS")
			}
		})
	}
}
