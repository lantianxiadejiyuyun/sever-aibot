package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

// Nodes tracks live control-plane connections from public relay nodes. It is
// intentionally separate from Hub: node credentials and device credentials
// must never be interchangeable.
type Nodes struct {
	store    *store.Store
	log      *slog.Logger
	mu       sync.RWMutex
	conns    map[string]*nodeConn
	onFrame  func(nodeID string, frame model.AgentFrame)
	onUp     func(nodeID string)
	onConfig func(nodeID string)
}

type nodeConn struct {
	nodeID   string
	ws       *websocket.Conn
	send     chan []byte
	done     chan struct{}
	lastSeen atomic.Int64
	tls      atomic.Bool
	once     sync.Once
}

func NewNodes(st *store.Store, log *slog.Logger) *Nodes {
	return &Nodes{store: st, log: log, conns: map[string]*nodeConn{}}
}

// SetFrameHandler receives bind results and other node frames.
func (n *Nodes) SetFrameHandler(fn func(nodeID string, frame model.AgentFrame)) {
	n.mu.Lock()
	n.onFrame = fn
	n.mu.Unlock()
}

// SetConnectHandler runs after a node is registered and its hello is stored,
// so the control plane can push the current config immediately.
func (n *Nodes) SetConnectHandler(fn func(nodeID string)) {
	n.mu.Lock()
	n.onUp = fn
	n.mu.Unlock()
}

// SetConfigRequestHandler answers a node's config_request frame.
func (n *Nodes) SetConfigRequestHandler(fn func(nodeID string)) {
	n.mu.Lock()
	n.onConfig = fn
	n.mu.Unlock()
}

func (n *Nodes) Online(nodeID string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.conns[nodeID]
	return ok
}

func (n *Nodes) OnlineCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.conns)
}

// TLS reports whether the node's tunnel listener uses TLS, as reported in
// its hello frame.
func (n *Nodes) TLS(nodeID string) bool {
	n.mu.RLock()
	c := n.conns[nodeID]
	n.mu.RUnlock()
	return c != nil && c.tls.Load()
}

func (n *Nodes) Disconnect(nodeID, reason string) {
	n.mu.RLock()
	c := n.conns[nodeID]
	n.mu.RUnlock()
	if c != nil {
		c.close(websocket.StatusPolicyViolation, reason)
	}
}

// Broadcast sends a frame to every connected node. It returns how many nodes
// accepted it. Used to revoke a device's tunnel sessions after key rotation.
func (n *Nodes) Broadcast(frame model.AgentFrame) int {
	n.mu.RLock()
	conns := make([]*nodeConn, 0, len(n.conns))
	for _, c := range n.conns {
		conns = append(conns, c)
	}
	n.mu.RUnlock()
	sent := 0
	for _, c := range conns {
		if c.sendFrame(frame) == nil {
			sent++
		}
	}
	return sent
}

func (n *Nodes) Send(nodeID string, frame model.AgentFrame) error {
	n.mu.RLock()
	c := n.conns[nodeID]
	n.mu.RUnlock()
	if c == nil {
		return ErrOffline
	}
	return c.sendFrame(frame)
}

func (c *nodeConn) sendFrame(frame model.AgentFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	select {
	case c.send <- body:
		return nil
	case <-c.done:
		return ErrOffline
	case <-time.After(2 * time.Second):
		c.close(websocket.StatusPolicyViolation, "send queue full")
		return errors.New("send queue full")
	}
}

func (c *nodeConn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close(code, reason)
	})
}

// Serve upgrades an authenticated node request. Authentication happens in the
// API layer; nodeID is trusted by the time Serve is called.
func (n *Nodes) Serve(nodeID string, w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		n.log.Warn("node websocket accept", "node", nodeID, "err", err)
		return
	}
	ws.SetReadLimit(1 << 20)
	c := &nodeConn{nodeID: nodeID, ws: ws, send: make(chan []byte, 32), done: make(chan struct{})}
	c.lastSeen.Store(time.Now().UnixNano())

	n.mu.Lock()
	if old := n.conns[nodeID]; old != nil {
		old.close(websocket.StatusGoingAway, "replaced by new connection")
	}
	n.conns[nodeID] = c
	onFrame, onUp := n.onFrame, n.onUp
	n.mu.Unlock()

	ctx := r.Context()
	if err := n.store.SetNodeOnline(ctx, nodeID); err != nil {
		n.log.Warn("mark node online", "node", nodeID, "err", err)
	}
	n.log.Info("node connected", "node", nodeID, "remote", r.RemoteAddr)
	if onUp != nil {
		go onUp(nodeID)
	}

	defer func() {
		c.close(websocket.StatusNormalClosure, "bye")
		n.mu.Lock()
		current := n.conns[nodeID] == c
		if current {
			delete(n.conns, nodeID)
		}
		n.mu.Unlock()
		if current {
			if err := n.store.SetNodeOffline(context.Background(), nodeID); err != nil {
				n.log.Warn("mark node offline", "node", nodeID, "err", err)
			}
			n.log.Info("node disconnected", "node", nodeID)
		}
	}()

	go n.writeLoop(ctx, c)
	go n.watchdog(ctx, c)

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		c.lastSeen.Store(time.Now().UnixNano())
		var frame model.AgentFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			n.log.Warn("bad node frame", "node", nodeID, "err", err)
			continue
		}
		switch frame.Type {
		case "hello":
			c.tls.Store(frame.TLS)
			if err := n.store.SetNodeOnline(ctx, nodeID); err != nil {
				n.log.Warn("save node hello", "node", nodeID, "err", err)
			}
			_ = c.sendFrame(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true})
			// The hello tells us whether the tunnel uses TLS. Refresh agent
			// configs so they dial the right scheme.
			if onUp != nil {
				go onUp(nodeID)
			}
		case "heartbeat":
			if err := n.store.TouchNode(ctx, nodeID); err != nil {
				n.log.Warn("touch node", "node", nodeID, "err", err)
			}
			_ = c.sendFrame(model.AgentFrame{Type: "heartbeat_ack", TS: time.Now().Unix(), OK: true})
		case "config_request":
			n.mu.RLock()
			fn := n.onConfig
			n.mu.RUnlock()
			if fn != nil {
				go fn(nodeID)
			}
		default:
			if onFrame != nil {
				onFrame(nodeID, frame)
			}
		}
	}
}

func (n *Nodes) writeLoop(ctx context.Context, c *nodeConn) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case body := <-c.send:
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(writeCtx, websocket.MessageText, body)
			cancel()
			if err != nil {
				c.close(websocket.StatusInternalError, "write failed")
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}

func (n *Nodes) watchdog(ctx context.Context, c *nodeConn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := time.Unix(0, c.lastSeen.Load())
			if time.Since(last) > 90*time.Second {
				c.close(websocket.StatusPolicyViolation, "heartbeat timeout")
				return
			}
		}
	}
}
