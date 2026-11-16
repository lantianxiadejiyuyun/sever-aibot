package tunnel

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestTicketRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	secret := Secret(key, "nod_1")
	if len(secret) != 32 {
		t.Fatalf("secret length = %d", len(secret))
	}
	token, err := Sign(secret, Ticket{
		DeviceID: "dev_1", NodeID: "nod_1", Forwards: []string{"fwd_1"}, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(secret, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "dev_1" || got.NodeID != "nod_1" || len(got.Forwards) != 1 {
		t.Fatalf("claims = %+v", got)
	}

	// A ticket signed for another node must not verify here.
	if _, err := Verify(Secret(key, "nod_2"), token, time.Now()); err == nil {
		t.Fatal("ticket verified with the wrong node secret")
	}
	// Tampering with the payload must fail.
	if _, err := Verify(secret, "x"+token, time.Now()); err == nil {
		t.Fatal("tampered ticket verified")
	}
	// Expired tickets must fail.
	expired, _ := Sign(secret, Ticket{
		DeviceID: "dev_1", NodeID: "nod_1", Forwards: []string{"fwd_1"}, ExpiresAt: time.Now().Add(-time.Second).Unix(),
	})
	if _, err := Verify(secret, expired, time.Now()); err == nil {
		t.Fatal("expired ticket verified")
	}
}

func TestProtocolRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAuth(&buf, AuthRequest{DeviceID: "dev_1", Ticket: "ticket"}); err != nil {
		t.Fatal(err)
	}
	auth, err := ReadAuth(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if auth.DeviceID != "dev_1" || auth.Ticket != "ticket" {
		t.Fatalf("auth = %+v", auth)
	}

	buf.Reset()
	if err := WriteResult(&buf, StatusDenied, "no"); err != nil {
		t.Fatal(err)
	}
	status, msg, err := ReadResult(&buf)
	if err != nil || status != StatusDenied || msg != "no" {
		t.Fatalf("result = %d %q %v", status, msg, err)
	}

	buf.Reset()
	if err := WriteForwardID(&buf, "fwd_1"); err != nil {
		t.Fatal(err)
	}
	id, err := ReadForwardID(&buf)
	if err != nil || id != "fwd_1" {
		t.Fatalf("forward id = %q %v", id, err)
	}

	if _, err := ReadAuth(bytes.NewReader([]byte("BADMAGIC"))); err == nil {
		t.Fatal("malformed handshake accepted")
	}
	if err := WriteForwardID(&buf, strings.Repeat("x", MaxForwardID+1)); err == nil {
		t.Fatal("oversized forward id accepted")
	}
}

func TestPipeCountOverYamux(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	type result struct {
		session *yamux.Session
		err     error
	}
	clientCh := make(chan result, 1)
	go func() {
		s, err := ClientSession(a)
		clientCh <- result{s, err}
	}()
	server, err := ServerSession(b)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	res := <-clientCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	client := res.session
	defer client.Close()

	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	echo, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = echo.Write([]byte("pong"))
		_ = echo.Close()
	}()
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("read %q", buf)
	}
}

func TestPipeCount(t *testing.T) {
	a1, a2 := tcpPair(t)
	b1, b2 := tcpPair(t)
	defer a2.Close()
	defer b2.Close()

	done := make(chan int64, 1)
	go func() { done <- PipeCount(a1, b1) }()

	if _, err := a2.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(b2, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("b side read %q", buf)
	}
	_ = a2.Close()
	_ = b2.Close()

	select {
	case n := <-done:
		if n < 4 {
			t.Fatalf("PipeCount = %d, want >= 4", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PipeCount did not finish")
	}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	return client, server
}
