package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

func (m *tunnelManager) startDirectProvider(session *yamux.Session, f model.TunnelForward) (*directService, error) {
	tcp, tcpErr := m.startTCPDirectProvider(session, f)
	if f.RendezvousAddress == "" {
		return tcp, tcpErr
	}
	udp, udpErr := m.startNATProvider(session, f)
	if udpErr != nil {
		if tcp != nil {
			return tcp, nil
		}
		return nil, errors.Join(tcpErr, udpErr)
	}
	if tcp != nil {
		tcp.extra = append(tcp.extra, udp)
		return tcp, nil
	}
	return udp, nil
}
func requestNAT(ctx context.Context, session *yamux.Session, f model.TunnelForward, pin string) (tunnel.NATCredential, error) {
	var credential tunnel.NATCredential
	stream, err := privateControl(session, f, "@nat")
	if err != nil {
		return credential, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if !f.Visitor {
		if err = json.NewEncoder(stream).Encode(tunnel.NATCredential{Fingerprint: pin}); err != nil {
			return credential, err
		}
	}
	status, _, err := tunnel.ReadResult(stream)
	if err != nil {
		return credential, err
	}
	if status != tunnel.StatusOK {
		return credential, tunnel.ErrNATUnavailable
	}
	err = json.NewDecoder(io.LimitReader(stream, 2048)).Decode(&credential)
	if err == nil && (len(credential.ID) != 64 || len(credential.Key) != 64 || len(credential.Fingerprint) != 64) {
		err = tunnel.ErrNATUnavailable
	}
	return credential, err
}
func (m *tunnelManager) startNATProvider(session *yamux.Session, f model.TunnelForward) (*directService, error) {
	server, err := net.ResolveUDPAddr("udp", f.RendezvousAddress)
	if err != nil {
		return nil, err
	}
	cert, pin, err := tunnel.DirectCertificate()
	if err != nil {
		return nil, err
	}
	packet, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, err
	}
	socket := tunnel.NewNATSocket(packet)
	listener, err := socket.Listen(cert)
	if err != nil {
		socket.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &directService{ctx: ctx, cancel: cancel, extra: []io.Closer{listener, socket}, conns: map[net.Conn]bool{}}
	registrationCtx, registrationCancel := context.WithTimeout(ctx, 3*time.Second)
	credential, err := requestNAT(registrationCtx, session, f, pin)
	registrationCancel()
	if err != nil {
		service.Close()
		return nil, err
	}
	peers := socket.Rendezvous(ctx, server, credential)
	go func() {
		for range peers {
		}
	}()
	go func() {
		for {
			conn, err := tunnel.AcceptNAT(ctx, listener)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
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
				dialCtx, cancel := context.WithTimeout(ctx, tunnelDialTimeout)
				target, err := m.policy.DialContext(dialCtx, "tcp", net.JoinHostPort(f.TargetIP, strconv.Itoa(f.TargetPort)))
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
func (m *tunnelManager) dialNAT(ctx context.Context, session *yamux.Session, f model.TunnelForward) (net.Conn, error) {
	credential, err := requestNAT(ctx, session, f, "")
	if err != nil {
		return nil, err
	}
	server, err := net.ResolveUDPAddr("udp", f.RendezvousAddress)
	if err != nil {
		return nil, err
	}
	packet, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, err
	}
	socket := tunnel.NewNATSocket(packet)
	success := false
	defer func() {
		if !success {
			socket.Close()
		}
	}()
	peers := socket.Rendezvous(ctx, server, credential)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case peer, ok := <-peers:
		if !ok {
			return nil, tunnel.ErrNATUnavailable
		}
		conn, err := socket.Dial(ctx, peer, credential.Fingerprint)
		if err != nil {
			return nil, err
		}
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		if err = tunnel.ProveDirect(conn, f.P2PKey, f.ID, f.Revision); err == nil {
			var status byte
			status, _, err = tunnel.ReadResult(conn)
			if err == nil && status != tunnel.StatusOK {
				err = tunnel.ErrNATUnavailable
			}
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		success = true
		return tunnel.OwnNATConn(conn, socket), nil
	}
}
