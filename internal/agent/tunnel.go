package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"server-aibot/internal/model"
	"server-aibot/internal/relay"
	"server-aibot/internal/tunnel"
)

const (
	tunnelDialTimeout  = 10 * time.Second
	tunnelHeaderLimit  = 15 * time.Second
	tunnelReconnectMin = time.Second
	tunnelReconnectMax = 30 * time.Second
)

// tunnelManager keeps one outbound tunnel per public node. Desired config is
// replaced wholesale on every config push; sessions that did not change are
// left alone so established connections survive unrelated updates.
type tunnelManager struct {
	deviceID string
	policy   *relay.Policy
	log      *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*tunnelSession
	notify   func()
}

type tunnelSession struct {
	node   model.TunnelNode
	cancel context.CancelFunc
}

func newTunnelManager(deviceID string, policy *relay.Policy, log *slog.Logger) *tunnelManager {
	if policy == nil {
		var err error
		policy, err = relay.NewPolicy(relay.Config{})
		if err != nil {
			// The default policy has no configurable parts and cannot fail;
			// keep the agent alive but unable to tunnel if it ever does.
			log.Error("relay policy", "err", err)
			policy = nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &tunnelManager{
		deviceID: deviceID, policy: policy, log: log,
		ctx: ctx, cancel: cancel, sessions: map[string]*tunnelSession{},
	}
}

// SetNotify installs a callback that asks the control session for a fresh
// config (and therefore fresh tickets). It is nil while disconnected.
func (m *tunnelManager) SetNotify(fn func()) {
	m.mu.Lock()
	m.notify = fn
	m.mu.Unlock()
}

func (m *tunnelManager) requestConfig() {
	m.mu.Lock()
	fn := m.notify
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Apply reconciles the desired node list.
func (m *tunnelManager) Apply(nodes []model.TunnelNode) {
	desired := make(map[string]model.TunnelNode, len(nodes))
	for _, n := range nodes {
		if n.NodeID == "" || n.Addr == "" || n.Ticket == "" || len(n.Forwards) == 0 {
			continue
		}
		desired[n.NodeID] = n
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, existing := range m.sessions {
		next, ok := desired[id]
		if ok && sameTunnelNode(existing.node, next) {
			// Tickets rotate on every config push. Update the copy used for
			// reconnects without disturbing a healthy session.
			existing.node.Ticket = next.Ticket
			delete(desired, id)
			continue
		}
		existing.cancel()
		delete(m.sessions, id)
	}
	for id, node := range desired {
		ctx, cancel := context.WithCancel(m.ctx)
		m.sessions[id] = &tunnelSession{node: node, cancel: cancel}
		go m.runSession(ctx, id)
	}
}

func sameTunnelNode(a, b model.TunnelNode) bool {
	if a.Addr != b.Addr || a.TLS != b.TLS || len(a.Forwards) != len(b.Forwards) {
		return false
	}
	for i := range a.Forwards {
		if a.Forwards[i] != b.Forwards[i] {
			return false
		}
	}
	return true
}

func (m *tunnelManager) Close() {
	m.cancel()
	m.mu.Lock()
	for id, s := range m.sessions {
		s.cancel()
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

func (m *tunnelManager) runSession(ctx context.Context, nodeID string) {
	attempt := 0
	for ctx.Err() == nil {
		m.mu.Lock()
		s := m.sessions[nodeID]
		var node model.TunnelNode
		if s != nil {
			node = s.node
		}
		m.mu.Unlock()
		if s == nil {
			return
		}
		err := m.connectOnce(ctx, node)
		if ctx.Err() != nil {
			return
		}
		delay := tunnelBackoff(attempt)
		attempt++
		m.log.Warn("tunnel disconnected",
			"device", m.deviceID,
			"node", node.NodeID,
			"addr", node.Addr,
			"err", err,
			"reconnectIn", delay.Round(time.Millisecond),
		)
		if sleepCtx(ctx, delay) != nil {
			return
		}
	}
}

func tunnelBackoff(attempt int) time.Duration {
	current := tunnelReconnectMin
	for i := 0; i < attempt && current < tunnelReconnectMax; i++ {
		current *= 2
	}
	if current > tunnelReconnectMax {
		current = tunnelReconnectMax
	}
	return fullJitter(current)
}

func (m *tunnelManager) connectOnce(ctx context.Context, node model.TunnelNode) error {
	if m.policy == nil {
		return fmt.Errorf("relay policy unavailable")
	}
	dialCtx, cancel := context.WithTimeout(ctx, tunnelDialTimeout)
	conn, err := m.dial(dialCtx, node)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(tunnelHeaderLimit))
	if err := tunnel.WriteAuth(conn, tunnel.AuthRequest{DeviceID: m.deviceID, Ticket: node.Ticket}); err != nil {
		return fmt.Errorf("auth write: %w", err)
	}
	status, message, err := tunnel.ReadResult(conn)
	if err != nil {
		return fmt.Errorf("auth read: %w", err)
	}
	if status != tunnel.StatusOK {
		if status == tunnel.StatusUnauthorized {
			// The ticket may have expired while the control socket stayed up.
			m.requestConfig()
		}
		return fmt.Errorf("node rejected tunnel: status=%d %s", status, message)
	}
	_ = conn.SetDeadline(time.Time{})

	session, err := tunnel.ServerSession(conn)
	if err != nil {
		return fmt.Errorf("yamux: %w", err)
	}
	defer session.Close()
	// AcceptStream has no context; close the session so shutdown does not
	// wait for the remote side.
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-sessionDone:
		}
	}()
	m.log.Info("tunnel connected", "device", m.deviceID, "node", node.NodeID, "addr", node.Addr)

	forwards := make(map[string]model.TunnelForward, len(node.Forwards))
	for _, f := range node.Forwards {
		forwards[f.ID] = f
	}
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}
		go m.handleStream(ctx, stream, forwards)
	}
}

func (m *tunnelManager) dial(ctx context.Context, node model.TunnelNode) (net.Conn, error) {
	if endpoint, ok := tunnelWSURL(node.Addr); ok {
		return dialTunnelWS(ctx, endpoint)
	}
	addr, host, err := nodeDialAddr(node.Addr, node.TLS)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: tunnelDialTimeout}
	if !node.TLS {
		return dialer.DialContext(ctx, "tcp", addr)
	}
	return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
}

// dialTunnelWS opens the tunnel through a reverse proxy. The proxy terminates
// TLS; the raw tunnel bytes ride inside binary WebSocket messages.
func dialTunnelWS(ctx context.Context, endpoint string) (net.Conn, error) {
	ws, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("tunnel websocket: %w", err)
	}
	// The WebSocket connection outlives the dial context; yamux closes it when
	// the session ends.
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}

// tunnelWSURL recognizes node addresses that reach the tunnel through a
// reverse proxy. Accepted forms: ws://host[:port][/path], wss://…, http://…
// and https://… (the last two are treated as ws/wss). A missing path means
// /tunnel.
func tunnelWSURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	var scheme, rest string
	switch {
	case len(s) >= 6 && strings.EqualFold(s[:6], "wss://"):
		scheme, rest = "wss", s[6:]
	case len(s) >= 5 && strings.EqualFold(s[:5], "ws://"):
		scheme, rest = "ws", s[5:]
	case len(s) >= 8 && strings.EqualFold(s[:8], "https://"):
		scheme, rest = "wss", s[8:]
	case len(s) >= 7 && strings.EqualFold(s[:7], "http://"):
		scheme, rest = "ws", s[7:]
	default:
		return "", false
	}
	host, path, _ := strings.Cut(rest, "/")
	if host == "" {
		return "", false
	}
	if path == "" {
		path = "tunnel"
	}
	return scheme + "://" + host + "/" + path, true
}

// nodeDialAddr returns the address to dial and the TLS server name. A bare
// host (no port) is accepted for TLS nodes so the console can show
// "node.example.com" and rely on the default 443.
func nodeDialAddr(raw string, tlsEnabled bool) (addr, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("node address is empty")
	}
	if h, _, splitErr := net.SplitHostPort(raw); splitErr == nil && h != "" {
		return raw, h, nil
	}
	bare := raw
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}
	if !tlsEnabled {
		return "", "", fmt.Errorf("node address %q must include a port", raw)
	}
	if strings.Count(bare, ":") > 1 && net.ParseIP(bare) == nil {
		return "", "", fmt.Errorf("invalid node address %q", raw)
	}
	return net.JoinHostPort(bare, "443"), bare, nil
}

func (m *tunnelManager) handleStream(ctx context.Context, stream *yamux.Stream, forwards map[string]model.TunnelForward) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(tunnelHeaderLimit))
	forwardID, err := tunnel.ReadForwardID(stream)
	if err != nil {
		return
	}
	f, ok := forwards[forwardID]
	if !ok {
		_ = tunnel.WriteResult(stream, tunnel.StatusUnknownForward, "forward not in config")
		return
	}
	if f.TargetIP == "" || f.TargetPort <= 0 {
		_ = tunnel.WriteResult(stream, tunnel.StatusBadRequest, "target missing")
		return
	}
	targetAddr := net.JoinHostPort(f.TargetIP, strconv.Itoa(f.TargetPort))
	target, err := m.policy.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		m.log.Warn("tunnel target dial failed", "device", m.deviceID, "forward", f.ID, "target", targetAddr, "err", err)
		_ = tunnel.WriteResult(stream, tunnel.StatusDialFailed, "target dial failed")
		return
	}
	defer target.Close()
	if err := tunnel.WriteResult(stream, tunnel.StatusOK, ""); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	tunnel.Pipe(stream, target)
}
