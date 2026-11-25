package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/yamux"
	"io"
	"net"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var errDirectRegistration = errors.New("direct registration pending")

func privateControl(session *yamux.Session, f model.TunnelForward, kind string) (*yamux.Stream, error) {
	stream, err := session.OpenStream()
	if err != nil {
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	if err = tunnel.WriteForwardID(stream, kind+":"+strconv.FormatInt(f.Revision, 10)+":"+f.ID); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}
func directReport(session *yamux.Session, f model.TunnelForward, kind string, value any) {
	stream, err := privateControl(session, f, kind)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = json.NewEncoder(stream).Encode(value)
}

type directService struct {
	ctx    context.Context
	cancel context.CancelFunc
	ln     net.Listener
	extra  []io.Closer
	mu     sync.Mutex
	conns  map[net.Conn]bool
	closed bool
	once   sync.Once
}

func (s *directService) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		if s.ln != nil {
			s.ln.Close()
		}
		// Send stream/connection closure before destroying the UDP transport.
		for c := range s.conns {
			tunnel.AbortDirect(c)
		}
		for _, closer := range s.extra {
			_ = closer.Close()
		}
	})
	return nil
}
func (m *tunnelManager) startDirectProviders(session *yamux.Session, forwards []model.TunnelForward) map[string]io.Closer {
	services := map[string]io.Closer{}
	for _, f := range forwards {
		if f.Kind != "p2p" || f.Visitor || f.DirectDisabled {
			continue
		}
		service, err := m.startDirectProvider(session, f)
		if err != nil {
			m.log.Warn("direct provider unavailable", "forward", f.ID, "err", err)
			if errors.Is(err, errDirectRegistration) {
				_ = session.Close()
			}
			continue
		}
		services[f.ID] = service
	}
	go func() {
		// Refresh pinned, short-lived certificates before they expire. Session
		// teardown closes old direct services before the manager reconnects.
		refresh := time.NewTimer(50 * time.Minute)
		defer refresh.Stop()
		select {
		case <-session.CloseChan():
		case <-refresh.C:
			if len(services) > 0 {
				_ = session.Close()
			}
		}
		for _, service := range services {
			service.Close()
		}
	}()
	return services
}
func (m *tunnelManager) startTCPDirectProvider(session *yamux.Session, f model.TunnelForward) (*directService, error) {
	cert, pin, err := tunnel.DirectCertificate()
	if err != nil {
		return nil, err
	}
	address := f.DirectListen
	if address == "" {
		address = "0.0.0.0:0"
	}
	ln, err := tls.Listen("tcp", address, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
	if err != nil {
		return nil, err
	}
	serviceCtx, cancelService := context.WithCancel(context.Background())
	service := &directService{ln: ln, conns: map[net.Conn]bool{}, ctx: serviceCtx, cancel: cancelService}
	candidates := tunnel.DirectCandidates{Fingerprint: pin}
	if f.DirectAddress != "" {
		candidates.Addresses = []string{f.DirectAddress}
	} else {
		port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		addresses, _ := net.InterfaceAddrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			candidate := net.JoinHostPort(ip.String(), port)
			if tunnel.ValidateDirectAddress(candidate, false) == nil {
				candidates.Addresses = append(candidates.Addresses, candidate)
				if len(candidates.Addresses) == 16 {
					break
				}
			}
		}
	}
	if len(candidates.Addresses) == 0 {
		service.Close()
		return nil, errors.New("no usable direct TCP candidates")
	}
	stream, err := privateControl(session, f, "@publish")
	if err != nil {
		service.Close()
		return nil, err
	}
	err = json.NewEncoder(stream).Encode(candidates)
	if err == nil {
		status, _, readErr := tunnel.ReadResult(stream)
		err = readErr
		if err == nil && status != tunnel.StatusOK {
			err = errors.New("direct publication rejected")
		}
	}
	stream.Close()
	if err != nil {
		service.Close()
		return nil, fmt.Errorf("%w: %v", errDirectRegistration, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			service.mu.Lock()
			if service.closed || len(service.conns) >= 128 {
				service.mu.Unlock()
				conn.Close()
				continue
			}
			service.conns[conn] = true
			service.mu.Unlock()
			go func() {
				defer func() { conn.Close(); service.mu.Lock(); delete(service.conns, conn); service.mu.Unlock() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if tunnel.AcceptDirect(conn, f.P2PKey, f.ID, f.Revision) != nil {
					return
				}
				ctx, cancel := context.WithTimeout(service.ctx, tunnelDialTimeout)
				target, err := m.policy.DialContext(ctx, "tcp", net.JoinHostPort(f.TargetIP, strconv.Itoa(f.TargetPort)))
				cancel()
				if err != nil {
					_ = tunnel.WriteResult(conn, tunnel.StatusDialFailed, "target unavailable")
					return
				}
				defer target.Close()
				if tunnel.WriteResult(conn, tunnel.StatusOK, "") != nil {
					return
				}
				_ = conn.SetDeadline(time.Time{})
				tunnel.Pipe(conn, target)
			}()
		}
	}()
	return service, nil
}
func (m *tunnelManager) dialDirect(session *yamux.Session, f model.TunnelForward) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	ready := make(chan result)
	go func() {
		conn, err := m.dialDirectContext(ctx, session, f)
		select {
		case ready <- result{conn, err}:
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
		}
	}()
	select {
	case r := <-ready:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *tunnelManager) dialDirectContext(ctx context.Context, session *yamux.Session, f model.TunnelForward) (net.Conn, error) {
	if f.DirectDisabled {
		return nil, errors.New("direct traffic requires relay policy enforcement")
	}
	if f.RendezvousAddress != "" {
		natCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
		conn, err := m.dialNAT(natCtx, session, f)
		cancel()
		if err == nil {
			return conn, nil
		}
	}
	deadline, _ := ctx.Deadline()
	stream, err := privateControl(session, f, "@lookup")
	if err != nil {
		return nil, err
	}
	_ = stream.SetDeadline(deadline)
	status, _, err := tunnel.ReadResult(stream)
	if err != nil || status != tunnel.StatusOK {
		stream.Close()
		return nil, errors.New("direct candidates unavailable")
	}
	var candidates tunnel.DirectCandidates
	err = json.NewDecoder(io.LimitReader(stream, 8192)).Decode(&candidates)
	stream.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates.Addresses) > 16 {
		return nil, errors.New("too many direct candidates")
	}
	for _, address := range candidates.Addresses {
		if tunnel.ValidateDirectAddress(address, address == f.DirectAddress) != nil {
			continue
		}
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: tunnel.PinnedTLS(candidates.Fingerprint)}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(deadline)
		if err = tunnel.ProveDirect(conn, f.P2PKey, f.ID, f.Revision); err == nil {
			var status byte
			status, _, err = tunnel.ReadResult(conn)
			if status != tunnel.StatusOK && err == nil {
				err = errors.New("direct target rejected")
			}
		}
		if err == nil {
			_ = conn.SetDeadline(time.Time{})
			return conn, nil
		}
		conn.Close()
	}
	return nil, errors.New("direct TCP candidates unreachable or rejected")
}
func (m *tunnelManager) pipeDirect(session *yamux.Session, f model.TunnelForward, local, direct net.Conn) {
	var upload, download atomic.Int64
	var workers sync.WaitGroup
	workers.Add(2)
	done := make(chan struct{})
	copyCount := func(dst, src net.Conn, count *atomic.Int64) {
		defer workers.Done()
		defer local.Close()
		defer direct.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				written, e := dst.Write(buf[:n])
				count.Add(int64(written))
				if e != nil || written != n {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go copyCount(direct, local, &upload)
	go copyCount(local, direct, &download)
	go func() { workers.Wait(); close(done) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	flush := func() {
		up, down := upload.Swap(0), download.Swap(0)
		if up+down > 0 {
			directReport(session, f, "@traffic", model.AgentFrame{UploadBytes: up, DownloadBytes: down})
		}
	}
	for {
		select {
		case <-ticker.C:
			flush()
		case <-session.CloseChan():
			local.Close()
			tunnel.AbortDirect(direct)
			<-done
			flush()
			return
		case <-done:
			flush()
			return
		}
	}
}
