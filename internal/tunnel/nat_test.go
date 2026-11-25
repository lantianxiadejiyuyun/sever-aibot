package tunnel

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mappedUDP models an endpoint-independent NAT mapping with port-restricted
// filtering: a peer cannot send inward until this endpoint sent to that peer.
// Its private local address differs from the actual public UDP source address.
// This deterministic fixture does not claim real router interoperability.
type mappedUDP struct {
	net.PacketConn
	mu        sync.Mutex
	permitted map[string]bool
	private   net.Addr
	dropped   atomic.Int64
}

func newMappedUDP(t *testing.T, port int) *mappedUDP {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &mappedUDP{PacketConn: conn, permitted: map[string]bool{}, private: &net.UDPAddr{IP: net.IPv4(10, 1, 0, 2), Port: port}}
}
func (c *mappedUDP) LocalAddr() net.Addr { return c.private }
func (c *mappedUDP) WriteTo(b []byte, a net.Addr) (int, error) {
	c.mu.Lock()
	c.permitted[a.String()] = true
	c.mu.Unlock()
	return c.PacketConn.WriteTo(b, a)
}
func (c *mappedUDP) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, a, e := c.PacketConn.ReadFrom(b)
		if e != nil {
			return n, a, e
		}
		c.mu.Lock()
		ok := c.permitted[a.String()]
		c.mu.Unlock()
		if ok {
			return n, a, nil
		}
		c.dropped.Add(1)
	}
}
func rendezvousFixture(t *testing.T) (*Rendezvous, net.Addr) {
	t.Helper()
	packet, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	r := NewRendezvous(packet)
	t.Cleanup(func() { r.Close() })
	return r, packet.LocalAddr()
}

func TestNATMappedSocketQUIC(t *testing.T) {
	r, server := rendezvousFixture(t)
	providerPacket := newMappedUDP(t, 10001)
	visitorPacket := newMappedUDP(t, 10002)
	provider := NewNATSocket(providerPacket)
	defer provider.Close()
	visitor := NewNATSocket(visitorPacket)
	defer visitor.Close()
	cert, pin, err := DirectCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := provider.Listen(cert)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	providerCredential, err := r.Issue("f:1", true, pin, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	visitorCredential, err := r.Issue("f:1", false, pin, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	providerPeers := provider.Rendezvous(ctx, server, providerCredential)
	visitorPeers := visitor.Rendezvous(ctx, server, visitorCredential)
	select {
	case addr := <-providerPeers:
		if addr.String() != visitorPacket.PacketConn.LocalAddr().String() {
			t.Fatalf("wrong observed visitor: %v", addr)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var peer net.Addr
	select {
	case peer = <-visitorPeers:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if peer.String() != providerPacket.PacketConn.LocalAddr().String() {
		t.Fatalf("wrong mapped address: %v", peer)
	}
	result := make(chan error, 1)
	payload := bytes.Repeat([]byte("reliable encrypted direct stream\n"), 8192)
	go func() {
		conn, e := AcceptNAT(ctx, listener)
		if e != nil {
			result <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		if e = AcceptDirect(conn, "key", "f", 1); e != nil {
			result <- e
			return
		}
		received := make([]byte, len(payload))
		_, e = io.ReadFull(conn, received)
		if e == nil && !bytes.Equal(received, payload) {
			e = io.ErrUnexpectedEOF
		}
		if e == nil {
			_, e = conn.Write([]byte("ok"))
		}
		if e == nil {
			var ack [1]byte
			_, e = io.ReadFull(conn, ack[:])
		}
		result <- e
	}()
	conn, err := visitor.Dial(ctx, peer, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if err = ProveDirect(conn, "key", "f", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if string(reply[:]) != "ok" {
		t.Fatal("bad response")
	}
	_, _ = conn.Write([]byte{1})
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

func TestRendezvousRejectsForgeryAndRevocation(t *testing.T) {
	r, server := rendezvousFixture(t)
	var revoked atomic.Bool
	credential, err := r.Issue("f", true, "pin", func() bool { return !revoked.Load() })
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	p := natPacket{Kind: "register", ID: credential.ID, Nonce: randomNATToken()}
	r.receive(encodeNAT(p, "forged"), packet.LocalAddr())
	r.mu.Lock()
	registered := r.registrations[credential.ID].address != nil
	r.mu.Unlock()
	if registered {
		t.Fatal("forged registration accepted")
	}
	r.receive(encodeNAT(p, credential.Key), packet.LocalAddr())
	r.mu.Lock()
	registered = r.registrations[credential.ID].address != nil
	r.mu.Unlock()
	if !registered {
		t.Fatal("valid registration rejected")
	}
	revoked.Store(true)
	if r.ProviderPin("f") != "" {
		t.Fatal("revoked provider discoverable")
	}
	// Valid signed packets for a revoked session cannot produce peer exchange.
	other, err := r.Issue("f", false, "pin", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	visitor := NewNATSocket(newMappedUDP(t, 10003))
	defer visitor.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	peers := visitor.Rendezvous(ctx, server, other)
	select {
	case addr, ok := <-peers:
		if ok {
			t.Fatalf("revoked address exchanged: %v", addr)
		}
	case <-ctx.Done():
	}
}
func TestNATCloseFlushesFinalResponse(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	provider := NewNATSocket(packet)
	defer provider.Close()
	cert, pin, err := DirectCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := provider.Listen(cert)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	clientPacket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client := NewNATSocket(clientPacket)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte("final response"), 32768)
	result := make(chan error, 1)
	go func() {
		conn, err := AcceptNAT(ctx, listener)
		if err != nil {
			result <- err
			return
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Write(payload)
		_ = conn.Close()
		result <- err
	}()
	conn, err := client.Dial(ctx, packet.LocalAddr(), pin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("truncated response: got %d want %d", len(got), len(payload))
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

func TestNATRejectsWrongCertificatePin(t *testing.T) {
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	provider := NewNATSocket(packet)
	defer provider.Close()
	cert, _, err := DirectCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := provider.Listen(cert)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	clientPacket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client := NewNATSocket(clientPacket)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, packet.LocalAddr(), "wrong pin")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("incorrect pin accepted")
	}
}
