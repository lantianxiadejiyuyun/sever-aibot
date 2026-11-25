// Package node implements the public relay-node runtime: it keeps a control
// socket to the panel, listens on the entry ports assigned to it, and splices
// public connections into tunnels opened by intranet agents.
package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

const (
	tunnelAuthTimeout = 15 * time.Second
	streamOpenTimeout = 10 * time.Second
)

// Runtime owns the tunnel listener and the per-forward entry listeners.
type Runtime struct {
	log          *slog.Logger
	nodeID       string
	tunnelListen string
	entryHost    string
	tlsConfig    *tls.Config
	httpTunnel   bool
	// TunnelTransport selects tcp (default), kcp, or quic before Start.
	TunnelTransport  string
	RendezvousListen string

	quotaMu   sync.Mutex
	quotaWait map[string]chan model.AgentFrame

	sendMu sync.RWMutex
	send   func(model.AgentFrame) error

	mu        sync.Mutex
	secret    []byte
	forwards  map[string]model.TunnelForward
	byPort    map[int]model.TunnelForward
	listeners map[int]*portListener
	sessions  map[string]*deviceTunnel

	applyMu              sync.Mutex
	configRevision       int64
	candidates           map[string]directRegistration
	pendingDirectRevokes map[string]string
	policy               model.NodeRuntimePolicy
	active               map[*activeEntry]bool
	pacer                bytePacer
	reportMu             sync.Mutex
	reports              map[string]model.AgentFrame

	ctx        context.Context
	cancel     context.CancelFunc
	tunnelLn   net.Listener
	rendezvous *tunnel.Rendezvous
	httpSrv    *http.Server
	hostServer *http.Server
	hostPort   int
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

type deviceTunnel struct {
	deviceID string
	session  *yamux.Session
	issuedAt int64
	allowed  map[string]bool
}

type portListener struct {
	port     int
	ln       net.Listener
	packet   *net.UDPConn
	protocol string
}

func (l *portListener) Close() {
	if l.ln != nil {
		_ = l.ln.Close()
	}
	if l.packet != nil {
		_ = l.packet.Close()
	}
}

// NewRuntime prepares a runtime. A non-nil tlsConfig makes the tunnel
// listener speak TLS; entry ports remain plain TCP for the forwarded service.
//
// httpTunnel serves the tunnel as a WebSocket at /tunnel instead of a raw
// byte stream. That lets a reverse proxy (nginx) terminate TLS and forward
// wss:// to this listener, which is how the panel-fronted deployment works.
// The TLS pair, when present, is applied to the HTTP listener itself.
func NewRuntime(nodeID, tunnelListen, entryHost string, tlsConfig *tls.Config, httpTunnel bool, log *slog.Logger) *Runtime {
	if entryHost == "" {
		entryHost = "0.0.0.0"
	}
	return &Runtime{
		log: log, nodeID: nodeID, tunnelListen: tunnelListen, entryHost: entryHost,
		tlsConfig: tlsConfig, httpTunnel: httpTunnel,
		forwards: map[string]model.TunnelForward{}, byPort: map[int]model.TunnelForward{},
		listeners: map[int]*portListener{}, sessions: map[string]*deviceTunnel{},
		active: map[*activeEntry]bool{}, reports: map[string]model.AgentFrame{}, candidates: map[string]directRegistration{}, pendingDirectRevokes: map[string]string{},
	}
}

// SetSender installs the control-socket writer used for bind results and
// traffic reports. Set nil when the control session ends.
func (r *Runtime) SetSender(fn func(model.AgentFrame) error) {
	r.sendMu.Lock()
	r.send = fn
	r.sendMu.Unlock()
}

func (r *Runtime) sendFrame(frame model.AgentFrame) {
	r.sendMu.RLock()
	fn := r.send
	r.sendMu.RUnlock()
	if fn == nil {
		return
	}
	if err := fn(frame); err != nil {
		r.log.Warn("send node frame", "type", frame.Type, "err", err)
	}
}

// Start opens the tunnel listener. Entry listeners are opened by config.
func (r *Runtime) Start(ctx context.Context) error {
	r.ctx, r.cancel = context.WithCancel(ctx)
	var ln net.Listener
	var err error
	if r.TunnelTransport == "kcp" || r.TunnelTransport == "quic" {
		if r.httpTunnel {
			r.cancel()
			return errors.New("UDP tunnel transport cannot use HTTP mode")
		}
		ln, err = tunnel.ListenUDP(r.TunnelTransport, r.tunnelListen, r.tlsConfig)
	} else if r.TunnelTransport != "" && r.TunnelTransport != "tcp" {
		r.cancel()
		return fmt.Errorf("unsupported tunnel transport %q", r.TunnelTransport)
	} else if r.tlsConfig != nil {
		ln, err = tls.Listen("tcp", r.tunnelListen, r.tlsConfig)
	} else {
		ln, err = net.Listen("tcp", r.tunnelListen)
	}
	if err != nil {
		r.cancel()
		return fmt.Errorf("node: listen tunnel %s: %w", r.tunnelListen, err)
	}
	r.tunnelLn = ln
	if r.RendezvousListen != "" {
		if err := r.startRendezvous(r.RendezvousListen); err != nil {
			_ = ln.Close()
			r.cancel()
			return fmt.Errorf("node: rendezvous: %w", err)
		}
	}
	if r.httpTunnel {
		mux := http.NewServeMux()
		mux.HandleFunc("/tunnel", r.serveWSTunnel)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		r.httpSrv = srv
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && r.ctx.Err() == nil {
				r.log.Warn("tunnel http server", "addr", r.tunnelListen, "err", err)
			}
		}()
		r.log.Info("node tunnel listener ready", "addr", r.tunnelListen, "tls", r.tlsConfig != nil, "http", true)
		return nil
	}
	r.log.Info("node tunnel listener ready", "addr", r.tunnelListen, "tls", r.tlsConfig != nil, "http", false)
	r.wg.Add(1)
	go r.acceptTunnels()
	return nil
}

// serveWSTunnel upgrades a proxied WebSocket to a byte stream and runs the
// same authenticated tunnel handshake as the raw TCP listener.
func (r *Runtime) serveWSTunnel(w http.ResponseWriter, req *http.Request) {
	ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// Agents are not browsers and send no Origin; never accept a browser
		// origin on this endpoint.
		OriginPatterns:  []string{},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		r.log.Warn("tunnel websocket accept", "err", err)
		return
	}
	// handleTunnel hands the connection to yamux and closes it itself on a
	// failed handshake, so the WebSocket must not be closed here.
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	r.handleTunnel(conn)
}

// Close stops every listener and tunnel.
func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		r.applyMu.Lock()
		defer r.applyMu.Unlock()
		if r.cancel != nil {
			r.cancel()
		}
		if r.hostServer != nil {
			_ = r.hostServer.Close()
		}
		if r.httpSrv != nil {
			_ = r.httpSrv.Close()
		}
		if r.rendezvous != nil {
			_ = r.rendezvous.Close()
		}
		if r.tunnelLn != nil {
			_ = r.tunnelLn.Close()
		}
		r.mu.Lock()
		for e := range r.active {
			e.cancel()
			_ = e.conn.Close()
		}
		for port, l := range r.listeners {
			l.Close()
			delete(r.listeners, port)
		}
		for id, s := range r.sessions {
			_ = s.session.Close()
			delete(r.sessions, id)
		}
		r.mu.Unlock()
	})
	r.wg.Wait()
}

// ApplyConfig replaces the complete desired state. Listeners and tunnels are
// reconciled rather than recreated wholesale so unrelated forwards survive.
func (r *Runtime) ApplyConfig(secretB64 string, forwards []model.TunnelForward) {
	r.ApplyConfiguration(model.AgentFrame{TunnelSecret: secretB64, Forwards: forwards})
}
func (r *Runtime) ApplyConfiguration(frame model.AgentFrame) {
	secretB64, forwards := frame.TunnelSecret, frame.Forwards
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.ctx != nil && r.ctx.Err() != nil {
		return
	}
	if frame.Revision > 0 && frame.Revision < r.configRevision {
		return
	}
	if frame.Revision > 0 {
		r.configRevision = frame.Revision
	}

	secret, err := base64.RawURLEncoding.DecodeString(secretB64)
	if err != nil || len(secret) != 32 {
		r.log.Warn("node config has invalid tunnel secret")
		return
	}

	policy := model.NodeRuntimePolicy{}
	if frame.NodePolicy != nil {
		policy = *frame.NodePolicy
	}
	rejected := []model.AgentFrame{}
	revoked := []model.AgentFrame{}
	desired := make(map[int]model.TunnelForward, len(forwards))
	fwdMap := make(map[string]model.TunnelForward, len(forwards))
	for _, f := range forwards {
		if f.ID == "" {
			continue
		}
		if f.Disabled {
			revoked = append(revoked, model.AgentFrame{Type: "revoke_result", ForwardID: f.ID, Revision: f.Revision, OK: true})
			continue
		}
		if (f.Kind == "tunnel" || f.Kind == "p2p" || f.Kind == "proxy") && f.DeviceID != "" && f.ClientID != "" && f.ClientID != f.DeviceID && (f.Protocol == "" || f.Protocol == "tcp") {
			fwdMap[f.ID] = f
			continue
		}
		if f.Kind == "host" && f.Domain != "" && policy.HTTPPort > 0 && policy.HTTPPort <= 65535 && f.DeviceID != "" && (f.Protocol == "" || f.Protocol == "tcp") {
			fwdMap[f.ID] = f
			continue
		}
		message := ""
		if f.Kind != "port" || (f.Protocol != "" && f.Protocol != "tcp" && f.Protocol != "udp") {
			message = "unsupported forward type or protocol"
		}
		if f.RemotePort < 1 || f.RemotePort > 65535 || f.DeviceID == "" {
			message = "invalid entry configuration"
		}
		if !allowedPort(policy.ForwardPorts, f.RemotePort) {
			message = "entry port outside node pool"
		}

		if _, exists := desired[f.RemotePort]; exists {
			message = "duplicate entry port"
		}
		if message != "" {
			rejected = append(rejected, model.AgentFrame{Type: "bind_result", ForwardID: f.ID, Revision: f.Revision, Message: message})
			continue
		}
		desired[f.RemotePort] = f
		fwdMap[f.ID] = f
	}

	var staleSessions []*yamux.Session
	type directRevoke struct {
		forward model.TunnelForward
		session *yamux.Session
	}
	directRevokes := []directRevoke{}
	r.mu.Lock()
	secretChanged := len(r.secret) > 0 && !bytes.Equal(r.secret, secret)
	directPolicyChanged := (policy.LimitTotalGB > 0 && policy.LimitTotalGB != r.policy.LimitTotalGB) ||
		(policy.LimiterMbps > 0 && policy.LimiterMbps != r.policy.LimiterMbps) ||
		(policy.Concurrency > 0 && policy.Concurrency != r.policy.Concurrency) ||
		(policy.Connections > 0 && policy.Connections != r.policy.Connections)
	r.secret = secret
	for id, old := range r.forwards {
		if old.Kind == "p2p" {
			next, ok := fwdMap[id]
			if !ok || next != old || directPolicyChanged {
				delete(r.candidates, id)
				if session := r.sessions[old.DeviceID]; session != nil {
					directRevokes = append(directRevokes, directRevoke{forward: old, session: session.session})
					staleSessions = append(staleSessions, session.session)
					delete(r.sessions, old.DeviceID)
				}
			}
		}
	}
	if policy.RevokedBefore == nil {
		policy.RevokedBefore = map[string]int64{}
	}
	for id, stamp := range r.policy.RevokedBefore {
		if stamp > policy.RevokedBefore[id] {
			policy.RevokedBefore[id] = stamp
		}
	}
	r.policy = policy
	for id, session := range r.sessions {
		if cutoff := policy.RevokedBefore[id]; cutoff > 0 && session.issuedAt <= cutoff {
			staleSessions = append(staleSessions, session.session)
			delete(r.sessions, id)
		}
	}
	for e := range r.active {
		next, ok := fwdMap[e.forward.ID]
		if !ok || next != e.forward {
			e.cancel()
			_ = e.conn.Close()
		}
	}
	r.forwards = fwdMap
	r.byPort = desired
	for port, l := range r.listeners {
		if f, ok := desired[port]; !ok || f.Protocol != l.protocol {
			l.Close()
			delete(r.listeners, port)
			r.log.Info("entry listener removed", "port", port)
		}
	}
	if secretChanged {
		for id, s := range r.sessions {
			staleSessions = append(staleSessions, s.session)
			delete(r.sessions, id)
		}
	}
	existing := make(map[int]bool, len(r.listeners))
	for port := range r.listeners {
		existing[port] = true
	}
	r.mu.Unlock()

	for _, revoke := range directRevokes {
		confirmed := false
		stream, err := revoke.session.OpenStream()
		if err == nil {
			_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
			if tunnel.WriteForwardID(stream, "@revoke-direct:"+strconv.FormatInt(revoke.forward.Revision, 10)+":"+revoke.forward.ID) == nil {
				status, _, err := tunnel.ReadResult(stream)
				confirmed = err == nil && status == tunnel.StatusOK
			}
			stream.Close()
		}
		if !confirmed {
			r.pendingDirectRevokes[revoke.forward.ID] = revoke.forward.DeviceID
		}
	}
	for _, s := range staleSessions {
		_ = s.Close()
	}

	type result struct {
		forward model.TunnelForward
		ok      bool
		message string
	}
	results := make([]result, 0, len(desired))
	for port, f := range desired {
		if existing[port] {
			results = append(results, result{forward: f, ok: true})
			continue
		}
		addr := net.JoinHostPort(r.entryHost, strconv.Itoa(port))
		l := &portListener{port: port, protocol: f.Protocol}
		var err error
		if f.Protocol == "udp" {
			var address *net.UDPAddr
			address, err = net.ResolveUDPAddr("udp", addr)
			if err == nil {
				l.packet, err = net.ListenUDP("udp", address)
			}
		} else {
			l.ln, err = net.Listen("tcp", addr)
		}
		if err != nil {
			results = append(results, result{forward: f, message: "监听入口端口失败: " + err.Error()})
			continue
		}

		r.mu.Lock()
		r.listeners[port] = l
		r.mu.Unlock()
		r.wg.Add(1)
		if f.Protocol == "udp" {
			go r.serveUDP(l)
		} else {
			go r.servePort(l)
		}
		r.log.Info("entry listener ready", "port", port, "forward", f.ID)
		results = append(results, result{forward: f, ok: true})
	}
	for _, res := range results {
		r.sendFrame(model.AgentFrame{
			Type: "bind_result", ForwardID: res.forward.ID, Revision: res.forward.Revision, OK: res.ok, Message: res.message,
		})
	}
	r.reconcileHosts(policy.HTTPPort, fwdMap)

	for _, f := range rejected {
		r.sendFrame(f)
	}
	for _, f := range revoked {
		if _, pending := r.pendingDirectRevokes[f.ForwardID]; pending {
			f.OK = false
			f.Message = "直连服务未确认关闭，保持撤销等待状态"
		}
		r.sendFrame(f)
	}
}

// KickDevice closes an agent's tunnel, for example after its key rotated.
func (r *Runtime) KickDevice(deviceID string) { r.RevokeDevice(deviceID, 0) }
func (r *Runtime) RevokeDevice(deviceID string, stamp int64) {
	r.mu.Lock()
	for _, f := range r.forwards {
		if f.Kind == "p2p" && f.ClientID == deviceID {
			if provider := r.sessions[f.DeviceID]; provider != nil {
				_ = provider.session.Close()
				delete(r.sessions, f.DeviceID)
			}
			delete(r.candidates, f.ID)
		}
	}
	if stamp > 0 {
		if r.policy.RevokedBefore == nil {
			r.policy.RevokedBefore = map[string]int64{}
		}
		if stamp > r.policy.RevokedBefore[deviceID] {
			r.policy.RevokedBefore[deviceID] = stamp
		}
	}
	for e := range r.active {
		if e.forward.DeviceID == deviceID || e.forward.ClientID == deviceID {
			e.cancel()
			_ = e.conn.Close()
		}
	}
	s := r.sessions[deviceID]
	delete(r.sessions, deviceID)
	r.mu.Unlock()
	if s != nil {
		_ = s.session.Close()
		r.log.Info("tunnel revoked", "device", deviceID)
	}
}

func (r *Runtime) acceptTunnels() {
	defer r.wg.Done()
	for {
		conn, err := r.tunnelLn.Accept()
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			r.log.Warn("tunnel accept", "err", err)
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handleTunnel(conn)
		}()
	}
}

// rejectTunnel lets the peer receive its rejection before tearing down a UDP
// connection, whose Close can discard queued writes. The existing auth deadline
// and runtime cancellation bound this wait even for a peer that never closes.
func rejectTunnel(conn net.Conn, status byte, message string) {
	defer conn.Close()
	if tunnel.WriteResult(conn, status, message) == nil {
		var b [1]byte
		_, _ = conn.Read(b[:])
	}
}

func (r *Runtime) handleTunnel(conn net.Conn) {
	stop := context.AfterFunc(r.ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(tunnelAuthTimeout))
	auth, err := tunnel.ReadAuth(conn)
	if err != nil {
		rejectTunnel(conn, tunnel.StatusBadRequest, "bad handshake")
		return
	}
	r.mu.Lock()
	secret := append([]byte(nil), r.secret...)
	r.mu.Unlock()
	if len(secret) == 0 {
		rejectTunnel(conn, tunnel.StatusUnauthorized, "node not configured")
		return
	}
	ticket, err := tunnel.Verify(secret, auth.Ticket, time.Now())
	r.mu.Lock()
	cutoff := r.policy.RevokedBefore[auth.DeviceID]
	r.mu.Unlock()
	if cutoff > 0 && ticket.IssuedAt <= cutoff {
		err = errors.New("ticket revoked")
	}
	if err != nil || ticket.DeviceID != auth.DeviceID || ticket.NodeID != r.nodeID {
		r.log.Warn("tunnel rejected", "device", auth.DeviceID, "err", err)
		rejectTunnel(conn, tunnel.StatusUnauthorized, "ticket rejected")
		return
	}
	if err := tunnel.WriteResult(conn, tunnel.StatusOK, ""); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	session, err := tunnel.ClientSession(conn)
	if err != nil {
		r.log.Warn("yamux session", "device", auth.DeviceID, "err", err)
		_ = conn.Close()
		return
	}
	allowed := map[string]bool{}
	for _, id := range ticket.Forwards {
		allowed[id] = true
	}
	dt := &deviceTunnel{deviceID: auth.DeviceID, session: session, allowed: allowed, issuedAt: ticket.IssuedAt}
	r.mu.Lock()
	if cutoff := r.policy.RevokedBefore[auth.DeviceID]; r.ctx.Err() != nil || (cutoff > 0 && ticket.IssuedAt <= cutoff) || !bytes.Equal(secret, r.secret) {
		r.mu.Unlock()
		_ = session.Close()
		return
	}
	old := r.sessions[auth.DeviceID]
	if old == nil && r.policy.MaxPoolCount > 0 && len(r.sessions) >= r.policy.MaxPoolCount {
		r.mu.Unlock()
		_ = session.Close()
		return
	}
	r.sessions[auth.DeviceID] = dt
	r.mu.Unlock()
	if old != nil {
		_ = old.session.Close()
	}
	go r.acceptPrivateStreams(dt)
	r.log.Info("tunnel connected", "device", auth.DeviceID)

	go func() {
		<-session.CloseChan()
		r.mu.Lock()
		if r.sessions[auth.DeviceID] == dt {
			delete(r.sessions, auth.DeviceID)
		}
		r.mu.Unlock()
		r.log.Info("tunnel closed", "device", auth.DeviceID)
	}()
}

func (r *Runtime) servePort(l *portListener) {
	defer r.wg.Done()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go r.handleEntry(conn, l.port)
	}
}

func (r *Runtime) handleEntry(conn net.Conn, port int) {
	r.mu.Lock()
	f, ok := r.byPort[port]
	r.mu.Unlock()
	if !ok {
		_ = conn.Close()
		return
	}
	r.handleForwardEntry(conn, f)
}
func (r *Runtime) handleForwardEntry(conn net.Conn, f model.TunnelForward, acknowledge ...bool) {
	r.mu.Lock()
	current, ok := r.forwards[f.ID]
	ok = ok && current == f
	var session *yamux.Session
	if ok {
		if s := r.sessions[f.DeviceID]; s != nil && s.allowed[f.ID] {
			session = s.session
		}
	}
	limit := r.policy.Concurrency
	if limit <= 0 || (r.policy.Connections > 0 && r.policy.Connections < limit) {
		limit = r.policy.Connections
	}
	if limit > 0 && len(r.active) >= limit {
		ok = false
	}
	entryCtx, cancel := context.WithCancel(r.ctx)
	entry := &activeEntry{forward: f, conn: conn, cancel: cancel}
	if ok && session != nil {
		r.active[entry] = true
	}
	r.mu.Unlock()
	defer func() { cancel(); r.mu.Lock(); delete(r.active, entry); r.mu.Unlock(); _ = conn.Close() }()
	if !ok || session == nil {
		_ = conn.Close()
		return
	}
	stream, err := session.OpenStream()
	if err != nil {
		r.log.Warn("open stream", "forward", f.ID, "err", err)
		_ = conn.Close()
		return
	}
	defer stream.Close()
	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		select {
		case <-entryCtx.Done():
			_ = stream.Close()
		case <-streamDone:
		}
	}()
	_ = stream.SetDeadline(time.Now().Add(streamOpenTimeout))
	if err := tunnel.WriteForwardID(stream, f.ID); err != nil {
		_ = stream.Close()
		_ = conn.Close()
		return
	}
	status, message, err := tunnel.ReadResult(stream)
	if err != nil || status != tunnel.StatusOK {
		r.log.Warn("agent rejected stream", "forward", f.ID, "status", status, "message", message)
		_ = stream.Close()
		_ = conn.Close()
		return
	}
	_ = stream.SetDeadline(time.Time{})
	if len(acknowledge) > 0 && acknowledge[0] {
		if tunnel.WriteResult(conn, tunnel.StatusOK, "") != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
	}
	if f.Protocol == "udp" {
		r.pipeTraffic(entryCtx, f, conn, &tunnel.DatagramConn{Conn: stream})
	} else {
		r.pipeTraffic(entryCtx, f, conn, stream)
	}
}
