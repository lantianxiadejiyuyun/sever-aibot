package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const natALPN = "aibot-p2p-v1"

// NATSocket owns a single UDP socket for rendezvous, simultaneous punching and
// encrypted reliable QUIC streams. Endpoint-dependent (symmetric) mappings and
// networks blocking UDP may not traverse; callers must bound attempts and relay.
type NATSocket struct {
	transport *quic.Transport
	packet    net.PacketConn
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
}

func NewNATSocket(packet net.PacketConn) *NATSocket {
	ctx, cancel := context.WithCancel(context.Background())
	return &NATSocket{transport: &quic.Transport{Conn: packet}, packet: packet, ctx: ctx, cancel: cancel}
}
func (s *NATSocket) Close() error {
	s.once.Do(func() { s.cancel(); _ = s.transport.Close(); _ = s.packet.Close() })
	return nil
}
func (s *NATSocket) Listen(cert tls.Certificate) (*quic.Listener, error) {
	return s.transport.Listen(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, NextProtos: []string{natALPN}}, natQUICConfig())
}
func natQUICConfig() *quic.Config {
	return &quic.Config{HandshakeIdleTimeout: 2 * time.Second, MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second, MaxIncomingStreams: 1, MaxIncomingUniStreams: -1}
}

// Rendezvous starts authenticated address exchange. The returned channel carries
// only server-authenticated observed peer addresses. Punch packets have no data
// authority: TLS certificate pinning and the forward challenge authenticate QUIC.
func (s *NATSocket) Rendezvous(ctx context.Context, server net.Addr, credential NATCredential) <-chan net.Addr {
	peers := make(chan net.Addr, 16)
	nonce := randomNATToken()
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		packet := encodeNAT(natPacket{Kind: "register", ID: credential.ID, Nonce: nonce}, credential.Key)
		for {
			_, _ = s.transport.WriteTo(packet, server)
			select {
			case <-ctx.Done():
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(peers)
		buf := make([]byte, 2048)
		for {
			n, from, err := s.transport.ReadNonQUICPacket(ctx, buf)
			if err != nil {
				return
			}
			if from.String() != server.String() {
				continue
			}
			p, ok := decodeNAT(buf[:n], credential.Key)
			if !ok || p.Kind != "peer" || p.ID != credential.ID || p.Nonce != nonce || ValidateDirectAddress(p.Address, true) != nil {
				continue
			}
			addr, err := net.ResolveUDPAddr("udp", p.Address)
			if err != nil {
				continue
			}
			// Both peers receive the rendezvous response and send from their QUIC socket.
			for i := 0; i < 3; i++ {
				_, _ = s.transport.WriteTo([]byte{0, 'p'}, addr)
			}
			select {
			case peers <- addr:
			default:
			}
		}
	}()
	return peers
}
func (s *NATSocket) Dial(ctx context.Context, address net.Addr, pin string) (net.Conn, error) {
	conf := PinnedTLS(pin)
	conf.NextProtos = []string{natALPN}
	conn, err := s.transport.Dial(ctx, address, conf, natQUICConfig())
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	// QUIC does not announce an opened stream until it carries data. Send a
	// version marker before waiting for the server's authentication challenge.
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetWriteDeadline(deadline)
	}
	if _, err = stream.Write([]byte{1}); err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	return &natStream{Stream: stream, conn: conn}, nil
}
func AcceptNAT(ctx context.Context, listener *quic.Listener) (net.Conn, error) {
	conn, err := listener.Accept(ctx)
	if err != nil {
		return nil, err
	}
	acceptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := conn.AcceptStream(acceptCtx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	var marker [1]byte
	if _, err = io.ReadFull(stream, marker[:]); err != nil || marker[0] != 1 {
		_ = conn.CloseWithError(0, "")
		return nil, ErrNATUnavailable
	}
	_ = stream.SetReadDeadline(time.Time{})
	return &natStream{Stream: stream, conn: conn}, nil
}

type natStream struct {
	*quic.Stream
	conn *quic.Conn
	once sync.Once
}

func (s *natStream) LocalAddr() net.Addr  { return s.conn.LocalAddr() }
func (s *natStream) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }
func (s *natStream) Close() error {
	s.once.Do(func() {
		// FIN keeps queued stream data retransmittable; RESET/CONNECTION_CLOSE
		// here would discard a target's final response before the peer reads it.
		_ = s.Stream.Close()
		s.Stream.CancelRead(0)
		go func() {
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-s.conn.Context().Done():
			case <-timer.C:
				_ = s.conn.CloseWithError(0, "")
			}
		}()
	})
	return nil
}
func (s *natStream) Abort() {
	s.Stream.CancelRead(0)
	s.Stream.CancelWrite(0)
	_ = s.conn.CloseWithError(0, "revoked")
}

// AbortDirect bypasses graceful FIN draining when authorization is revoked.
func AbortDirect(conn net.Conn) {
	if aborter, ok := conn.(interface{ Abort() }); ok {
		aborter.Abort()
		return
	}
	_ = conn.Close()
}

// OwnNATConn closes the punching socket when the one visitor stream ends.
func OwnNATConn(conn net.Conn, socket *NATSocket) net.Conn {
	return &ownedNATConn{Conn: conn, socket: socket}
}

type ownedNATConn struct {
	net.Conn
	socket *NATSocket
	once   sync.Once
}

func (c *ownedNATConn) Close() error {
	c.once.Do(func() {
		_ = c.Conn.Close()
		if stream, ok := c.Conn.(*natStream); ok {
			go func() { <-stream.conn.Context().Done(); _ = c.socket.Close() }()
		} else {
			_ = c.socket.Close()
		}
	})
	return nil
}
func (c *ownedNATConn) Abort() { AbortDirect(c.Conn); _ = c.socket.Close() }

var ErrNATUnavailable = errors.New("UDP NAT traversal unavailable")
