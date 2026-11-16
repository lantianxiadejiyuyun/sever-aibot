package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	helloTimeout = 10 * time.Second
	dialTimeout  = 10 * time.Second
)

// Config describes the SNI router. Routes maps a TLS server name to an
// upstream host:port. DefaultUpstream, when set, receives every name that has
// no explicit route; otherwise unknown names are refused.
type Config struct {
	Listen          string
	Routes          map[string]string
	DefaultUpstream string
	Log             *slog.Logger
}

// Server accepts TLS connections on one port and forwards them, byte for byte,
// to the upstream chosen by the ClientHello server name.
type Server struct {
	listenAddr string
	routes     map[string]string
	def        string
	log        *slog.Logger

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// New validates the configuration and returns a server.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Listen) == "" {
		return nil, errors.New("edge listen address is required")
	}
	if len(cfg.Routes) == 0 {
		return nil, errors.New("at least one edge route is required")
	}
	routes := make(map[string]string, len(cfg.Routes))
	for name, upstream := range cfg.Routes {
		host := normalizeHost(name)
		if host == "" {
			return nil, errors.New("edge route has an empty server name")
		}
		if err := validateUpstream(upstream); err != nil {
			return nil, fmt.Errorf("route %s: %w", host, err)
		}
		routes[host] = strings.TrimSpace(upstream)
	}
	def := strings.TrimSpace(cfg.DefaultUpstream)
	if def != "" {
		if err := validateUpstream(def); err != nil {
			return nil, fmt.Errorf("default upstream: %w", err)
		}
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		listenAddr: strings.TrimSpace(cfg.Listen),
		routes:     routes,
		def:        def,
		log:        log,
		conns:      make(map[net.Conn]struct{}),
	}, nil
}

func validateUpstream(upstream string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(upstream))
	if err != nil {
		return fmt.Errorf("upstream %q must be host:port: %w", upstream, err)
	}
	if host == "" || port == "" {
		return fmt.Errorf("upstream %q must be host:port", upstream)
	}
	return nil
}

// ParseRoutes parses "a.example.com=127.0.0.1:7400,b.example.com=127.0.0.1:7000".
func ParseRoutes(spec string) (map[string]string, error) {
	routes := make(map[string]string)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, upstream, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("route %q must be name=host:port", part)
		}
		name = normalizeHost(name)
		upstream = strings.TrimSpace(upstream)
		if name == "" || upstream == "" {
			return nil, fmt.Errorf("route %q must be name=host:port", part)
		}
		if _, dup := routes[name]; dup {
			return nil, fmt.Errorf("duplicate route for %s", name)
		}
		routes[name] = upstream
	}
	if len(routes) == 0 {
		return nil, errors.New("no routes configured")
	}
	return routes, nil
}

// Route returns the upstream for a server name.
func (s *Server) Route(host string) (string, bool) {
	if upstream, ok := s.routes[normalizeHost(host)]; ok {
		return upstream, true
	}
	return s.def, s.def != ""
}

// ListenAndServe binds the listener and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	s.log.Info("edge listening", "addr", ln.Addr().String(), "routes", len(s.routes))

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		s.closeConns()
	}()

	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if delay == 0 {
				delay = 50 * time.Millisecond
			} else if delay < 2*time.Second {
				delay *= 2
			}
			s.log.Warn("edge accept failed", "err", err, "retryIn", delay)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	if !s.track(conn) {
		_ = conn.Close()
		return
	}
	defer func() {
		s.untrack(conn)
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	host, prefix, err := ReadClientHello(conn)
	if err != nil {
		s.log.Debug("edge handshake rejected", "remote", conn.RemoteAddr().String(), "err", err)
		return
	}
	upstream, ok := s.Route(host)
	if !ok {
		s.log.Warn("edge no route for server name", "server_name", host, "remote", conn.RemoteAddr().String())
		return
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	up, err := dialer.DialContext(ctx, "tcp", upstream)
	if err != nil {
		s.log.Warn("edge upstream dial failed", "server_name", host, "upstream", upstream, "err", err)
		return
	}
	defer up.Close()
	if !s.track(up) {
		return
	}
	defer s.untrack(up)

	_ = conn.SetReadDeadline(time.Time{})
	if _, err := up.Write(prefix); err != nil {
		return
	}
	s.log.Debug("edge routed", "server_name", host, "upstream", upstream, "remote", conn.RemoteAddr().String())
	proxy(conn, up)
}

func proxy(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}

func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *Server) closeConns() {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		_ = c.Close()
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()
}
