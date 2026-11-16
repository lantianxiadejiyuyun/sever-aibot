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

	sendMu sync.RWMutex
	send   func(model.AgentFrame) error

	mu        sync.Mutex
	secret    []byte
	forwards  map[string]model.TunnelForward
	byPort    map[int]model.TunnelForward
	listeners map[int]*portListener
	sessions  map[string]*deviceTunnel

	applyMu sync.Mutex

	ctx       context.Context
	cancel    context.CancelFunc
	tunnelLn  net.Listener
	httpSrv   *http.Server
	closeOnce sync.Once
	wg        sync.WaitGroup
}

type deviceTunnel struct {
	deviceID string
	session  *yamux.Session
}

type portListener struct {
	port int
	ln   net.Listener
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
	if r.tlsConfig != nil {
		ln, err = tls.Listen("tcp", r.tunnelListen, r.tlsConfig)
	} else {
		ln, err = net.Listen("tcp", r.tunnelListen)
	}
	if err != nil {
		r.cancel()
		return fmt.Errorf("node: listen tunnel %s: %w", r.tunnelListen, err)
	}
	r.tunnelLn = ln
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
		if r.cancel != nil {
			r.cancel()
		}
		if r.httpSrv != nil {
			_ = r.httpSrv.Close()
		}
		if r.tunnelLn != nil {
			_ = r.tunnelLn.Close()
		}
		r.mu.Lock()
		for port, l := range r.listeners {
			_ = l.ln.Close()
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
	r.applyMu.Lock()
	defer r.applyMu.Unlock()

	secret, err := base64.RawURLEncoding.DecodeString(secretB64)
	if err != nil || len(secret) != 32 {
		r.log.Warn("node config has invalid tunnel secret")
		return
	}

	desired := make(map[int]model.TunnelForward, len(forwards))
	fwdMap := make(map[string]model.TunnelForward, len(forwards))
	for _, f := range forwards {
		if f.ID == "" || f.Kind != "port" || f.RemotePort <= 0 || f.DeviceID == "" {
			continue
		}
		desired[f.RemotePort] = f
		fwdMap[f.ID] = f
	}

	var staleSessions []*yamux.Session
	r.mu.Lock()
	secretChanged := len(r.secret) > 0 && !bytes.Equal(r.secret, secret)
	r.secret = secret
	r.forwards = fwdMap
	r.byPort = desired
	for port, l := range r.listeners {
		if _, ok := desired[port]; !ok {
			_ = l.ln.Close()
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
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			r.log.Warn("entry listener failed", "port", port, "err", err)
			results = append(results, result{forward: f, ok: false, message: "监听入口端口失败: " + err.Error()})
			continue
		}
		l := &portListener{port: port, ln: ln}
		r.mu.Lock()
		r.listeners[port] = l
		r.mu.Unlock()
		r.wg.Add(1)
		go r.servePort(l)
		r.log.Info("entry listener ready", "port", port, "forward", f.ID)
		results = append(results, result{forward: f, ok: true})
	}
	for _, res := range results {
		r.sendFrame(model.AgentFrame{
			Type: "bind_result", ForwardID: res.forward.ID, OK: res.ok, Message: res.message,
		})
	}
}

// KickDevice closes an agent's tunnel, for example after its key rotated.
func (r *Runtime) KickDevice(deviceID string) {
	r.mu.Lock()
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

func (r *Runtime) handleTunnel(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(tunnelAuthTimeout))
	auth, err := tunnel.ReadAuth(conn)
	if err != nil {
		_ = tunnel.WriteResult(conn, tunnel.StatusBadRequest, "bad handshake")
		_ = conn.Close()
		return
	}
	r.mu.Lock()
	secret := append([]byte(nil), r.secret...)
	r.mu.Unlock()
	if len(secret) == 0 {
		_ = tunnel.WriteResult(conn, tunnel.StatusUnauthorized, "node not configured")
		_ = conn.Close()
		return
	}
	ticket, err := tunnel.Verify(secret, auth.Ticket, time.Now())
	if err != nil || ticket.DeviceID != auth.DeviceID || ticket.NodeID != r.nodeID {
		r.log.Warn("tunnel rejected", "device", auth.DeviceID, "err", err)
		_ = tunnel.WriteResult(conn, tunnel.StatusUnauthorized, "ticket rejected")
		_ = conn.Close()
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
	dt := &deviceTunnel{deviceID: auth.DeviceID, session: session}
	r.mu.Lock()
	old := r.sessions[auth.DeviceID]
	r.sessions[auth.DeviceID] = dt
	r.mu.Unlock()
	if old != nil {
		_ = old.session.Close()
	}
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
	var session *yamux.Session
	if ok {
		if s := r.sessions[f.DeviceID]; s != nil {
			session = s.session
		}
	}
	r.mu.Unlock()
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
	bytes := tunnel.PipeCount(conn, stream)
	if bytes > 0 {
		r.sendFrame(model.AgentFrame{Type: "traffic", ForwardID: f.ID, TrafficBytes: bytes})
	}
}
