package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
	kcp "github.com/xtaci/kcp-go/v5"
)

const tunnelALPN = "aibot-tunnel/1"

// UDPAddress recognizes secure UDP tunnel endpoints. Both transports require
// an explicit port and a certificate valid for the advertised hostname.
func UDPAddress(raw string) (transport, address, hostname string, err error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "kcp://") && !strings.HasPrefix(lower, "quic://") {
		return "", "", "", nil
	}
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", "", parseErr
	}
	if u.Scheme != "kcp" && u.Scheme != "quic" {
		return "", "", "", nil
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() == "" || u.Port() == "" {
		return "", "", "", errors.New("UDP tunnel address must be kcp://host:port or quic://host:port")
	}
	return u.Scheme, u.Host, u.Hostname(), nil
}

func transportTLS(cfg *tls.Config) (*tls.Config, error) {
	if cfg == nil {
		return nil, errors.New("KCP and QUIC tunnels require TLS configuration")
	}
	c := cfg.Clone()
	c.MinVersion = tls.VersionTLS13
	c.NextProtos = []string{tunnelALPN}
	return c, nil
}

func quicConfig() *quic.Config {
	return &quic.Config{HandshakeIdleTimeout: 10 * time.Second, MaxIdleTimeout: 60 * time.Second, KeepAlivePeriod: 20 * time.Second, MaxIncomingStreams: 1, MaxIncomingUniStreams: -1}
}

// DialUDP authenticates the node with TLS before any bearer ticket is sent.
// The returned connection outlives ctx, which controls only establishment.
func DialUDP(ctx context.Context, transport, address string, cfg *tls.Config) (net.Conn, error) {
	c, err := transportTLS(cfg)
	if err != nil {
		return nil, err
	}
	switch transport {
	case "kcp":
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("node hostname has no addresses")
		}
		raw, err := kcp.DialWithOptions(net.JoinHostPort(ips[0], port), nil, 10, 3)
		if err != nil {
			return nil, err
		}
		raw.SetStreamMode(true)
		conn := tls.Client(raw, c)
		if err := conn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return conn, nil
	case "quic":
		conn, err := quic.DialAddr(ctx, address, c, quicConfig())
		if err != nil {
			return nil, err
		}
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			conn.CloseWithError(0, "")
			return nil, err
		}
		return &quicStreamConn{Stream: stream, conn: conn}, nil
	default:
		return nil, fmt.Errorf("unsupported UDP tunnel transport %q", transport)
	}
}

// ListenUDP exposes the same byte-stream interface used by ticket auth and
// yamux. KCP uses TLS 1.3 over its reliable stream; QUIC uses native TLS 1.3.
func ListenUDP(transport, address string, cfg *tls.Config) (net.Listener, error) {
	c, err := transportTLS(cfg)
	if err != nil {
		return nil, err
	}
	switch transport {
	case "kcp":
		ln, err := kcp.ListenWithOptions(address, nil, 10, 3)
		if err != nil {
			return nil, err
		}
		return tls.NewListener(&kcpListener{ln}, c), nil
	case "quic":
		ln, err := quic.ListenAddr(address, c, quicConfig())
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		l := &quicListener{listener: ln, ctx: ctx, cancel: cancel, ready: make(chan net.Conn)}
		l.wg.Add(1)
		go l.run()
		return l, nil
	default:
		return nil, fmt.Errorf("unsupported UDP tunnel transport %q", transport)
	}
}

type kcpListener struct{ *kcp.Listener }

func (l *kcpListener) Accept() (net.Conn, error) {
	c, err := l.AcceptKCP()
	if err != nil {
		return nil, err
	}
	c.SetStreamMode(true)
	return c, nil
}

type quicStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func (c *quicStreamConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *quicStreamConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }
func (c *quicStreamConn) Close() error         { return c.conn.CloseWithError(0, "") }

type quicListener struct {
	listener *quic.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan net.Conn
	wg       sync.WaitGroup
}

func (l *quicListener) Addr() net.Addr { return l.listener.Addr() }
func (l *quicListener) Close() error {
	l.cancel()
	err := l.listener.Close()
	l.wg.Wait()
	return err
}
func (l *quicListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ready:
		return c, nil
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	}
}
func (l *quicListener) run() {
	defer l.wg.Done()
	defer l.cancel()
	for {
		c, err := l.listener.Accept(l.ctx)
		if err != nil {
			return
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			ctx, cancel := context.WithTimeout(l.ctx, 15*time.Second)
			defer cancel()
			stream, err := c.AcceptStream(ctx)
			if err != nil {
				c.CloseWithError(0, "")
				return
			}
			select {
			case l.ready <- &quicStreamConn{Stream: stream, conn: c}:
			case <-ctx.Done():
				c.CloseWithError(0, "")
			}
		}()
	}
}
