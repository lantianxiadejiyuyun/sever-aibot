package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

// Exercise real UDP sockets, verified TLS, ticket authorization, yamux, and
// the same forwarding handler used by the public entry listeners.
func TestUDPTunnelRoundTrip(t *testing.T) {
	for _, transport := range []string{"kcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			certServer := httptest.NewTLSServer(nil)
			cert := certServer.TLS.Certificates[0]
			roots := x509.NewCertPool()
			roots.AddCert(certServer.Certificate())
			certServer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt := NewRuntime("node", "127.0.0.1:0", "127.0.0.1", &tls.Config{Certificates: []tls.Certificate{cert}}, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rt.TunnelTransport = transport
			if err := rt.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			secret := bytes.Repeat([]byte{42}, 32)
			f := model.TunnelForward{ID: "forward", Kind: "tunnel", DeviceID: "device", ClientID: "visitor", Protocol: "tcp"}
			forbidden := f
			forbidden.ID = "not-in-ticket"
			rt.ApplyConfig(base64.RawURLEncoding.EncodeToString(secret), []model.TunnelForward{f, forbidden})
			dial := func(cfg *tls.Config) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return tunnel.DialUDP(ctx, transport, rt.tunnelLn.Addr().String(), cfg)
			}
			clientTLS := &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"}
			badTLS := clientTLS.Clone()
			badTLS.ServerName = "wrong.example"
			if c, err := dial(badTLS); err == nil {
				c.Close()
				t.Fatal("accepted certificate for wrong hostname")
			}
			badTLS = clientTLS.Clone()
			badTLS.RootCAs = x509.NewCertPool()
			if c, err := dial(badTLS); err == nil {
				c.Close()
				t.Fatal("accepted untrusted certificate")
			}

			rejected, err := dial(clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			rejected.SetDeadline(time.Now().Add(3 * time.Second))
			if err := tunnel.WriteAuth(rejected, tunnel.AuthRequest{DeviceID: "device", Ticket: "invalid"}); err != nil {
				t.Fatal(err)
			}
			status, _, err := tunnel.ReadResult(rejected)
			rejected.Close()
			if err != nil || status != tunnel.StatusUnauthorized {
				t.Fatalf("invalid ticket: status=%d err=%v", status, err)
			}

			conn, err := dial(clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ticket, err := tunnel.Sign(secret, tunnel.Ticket{DeviceID: "device", NodeID: "node", Forwards: []string{f.ID}, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if err := tunnel.WriteAuth(conn, tunnel.AuthRequest{DeviceID: "device", Ticket: ticket}); err != nil {
				t.Fatal(err)
			}
			status, _, err = tunnel.ReadResult(conn)
			if err != nil || status != tunnel.StatusOK {
				t.Fatalf("auth: %d %v", status, err)
			}
			conn.SetDeadline(time.Time{})
			session, err := tunnel.ServerSession(conn)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			deadline := time.Now().Add(3 * time.Second)
			for {
				rt.mu.Lock()
				ready := rt.sessions["device"] != nil
				rt.mu.Unlock()
				if ready {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("session not installed")
				}
				time.Sleep(time.Millisecond)
			}
			deniedPublic, deniedEntry := net.Pipe()
			go rt.handleForwardEntry(deniedEntry, forbidden)
			deniedPublic.SetReadDeadline(time.Now().Add(time.Second))
			var deniedByte [1]byte
			if _, err := deniedPublic.Read(deniedByte[:]); err != io.EOF {
				t.Fatalf("forward outside ticket was not closed: %v", err)
			}
			deniedPublic.Close()
			served := make(chan error, 1)
			go func() {
				stream, err := session.AcceptStream()
				if err != nil {
					served <- err
					return
				}
				defer stream.Close()
				stream.SetDeadline(time.Now().Add(5 * time.Second))
				id, err := tunnel.ReadForwardID(stream)
				if err != nil {
					served <- err
					return
				}
				if id != f.ID {
					served <- io.ErrUnexpectedEOF
					return
				}
				if err := tunnel.WriteResult(stream, tunnel.StatusOK, ""); err != nil {
					served <- err
					return
				}
				_, err = io.Copy(stream, stream)
				served <- err
			}()
			public, entry := net.Pipe()
			go rt.handleForwardEntry(entry, f)
			public.SetDeadline(time.Now().Add(5 * time.Second))
			payload := bytes.Repeat([]byte("verified UDP tunnel payload\n"), 8192)
			writeDone := make(chan error, 1)
			go func() { _, err := public.Write(payload); writeDone <- err }()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(public, got); err != nil {
				t.Fatal(err)
			}
			if err := <-writeDone; err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("corrupted roundtrip")
			}
			public.Close()
			select {
			case err := <-served:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("forward did not close")
			}

			// A connected peer that never authenticates cannot delay runtime shutdown.
			idle, err := dial(clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer idle.Close()
			if transport == "quic" {
				idle.Write([]byte{0})
			}
			rt.mu.Lock()
			nodeSession := rt.sessions["device"].session
			rt.mu.Unlock()
			done := make(chan struct{})
			go func() { rt.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("runtime shutdown blocked by unauthenticated peer")
			}
			select {
			case <-nodeSession.CloseChan():
			case <-time.After(2 * time.Second):
				t.Fatal("authenticated session survived shutdown")
			}
		})
	}
}
