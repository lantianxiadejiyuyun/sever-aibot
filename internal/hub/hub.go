// Package hub tracks live agent WebSocket connections and exchanges control
// frames with them. Data-plane traffic does not use this socket: agents dial
// the public node directly and multiplex target connections with yamux.
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

// ErrOffline means the device has no live WebSocket.
var ErrOffline = errors.New("device offline")

// Conn is one connected agent.
type Conn struct {
	DeviceID string
	ws       *websocket.Conn
	send     chan []byte
	done     chan struct{}
	lastSeen atomic.Int64 // unix nano
	once     sync.Once
}

// Send queues a control frame. It never blocks a caller for long: a full queue
// closes the connection rather than delaying the request path.
func (c *Conn) Send(frame model.AgentFrame) error {
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
		c.Close(websocket.StatusPolicyViolation, "send queue full")
		return errors.New("send queue full")
	}
}

func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.Close(code, reason)
	})
}

// Hub owns the device→connection table.
type Hub struct {
	store    *store.Store
	log      *slog.Logger
	mu       sync.RWMutex
	conns    map[string]*Conn
	onUp     func(deviceID string)
	onConfig func(deviceID string)
}

func New(st *store.Store, log *slog.Logger) *Hub {
	return &Hub{store: st, log: log, conns: map[string]*Conn{}}
}

// SetConnectHandler runs after an agent socket is registered and marked
// online, so the control plane can push the current tunnel config.
func (h *Hub) SetConnectHandler(fn func(deviceID string)) {
	h.mu.Lock()
	h.onUp = fn
	h.mu.Unlock()
}

// SetConfigRequestHandler answers an agent's config_request frame.
func (h *Hub) SetConfigRequestHandler(fn func(deviceID string)) {
	h.mu.Lock()
	h.onConfig = fn
	h.mu.Unlock()
}

func (h *Hub) Online(deviceID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[deviceID]
	return ok
}

// Disconnect closes a device's live socket. Used after key rotation and
// administrative disable so a revoked credential cannot keep a session alive.
func (h *Hub) Disconnect(deviceID, reason string) {
	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()
	if c != nil {
		c.Close(websocket.StatusPolicyViolation, reason)
	}
}

func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Send delivers a frame to a specific device.
func (h *Hub) Send(deviceID string, frame model.AgentFrame) error {
	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return ErrOffline
	}
	return c.Send(frame)
}

// Serve upgrades an authenticated request. Authentication happens in the API
// layer; by the time Serve is called deviceID is trusted.
func (h *Hub) Serve(deviceID string, w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Agents are not browsers and do not send Origin. Do not accept
		// browser origins here.
		OriginPatterns:  []string{},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		h.log.Warn("agent websocket accept", "device", deviceID, "err", err)
		return
	}
	ws.SetReadLimit(1 << 20)
	c := &Conn{DeviceID: deviceID, ws: ws, send: make(chan []byte, 32), done: make(chan struct{})}
	c.lastSeen.Store(time.Now().UnixNano())

	h.mu.Lock()
	if old := h.conns[deviceID]; old != nil {
		// A reconnect replaces the old socket; the old read loop will exit.
		old.Close(websocket.StatusGoingAway, "replaced by new connection")
	}
	h.conns[deviceID] = c
	onUp := h.onUp
	h.mu.Unlock()

	ctx := r.Context()
	if err := h.store.SetDeviceOnline(ctx, deviceID, "", "", ""); err != nil {
		h.log.Warn("mark device online", "device", deviceID, "err", err)
	}
	h.log.Info("agent connected", "device", deviceID, "remote", r.RemoteAddr)
	if onUp != nil {
		go onUp(deviceID)
	}

	defer func() {
		c.Close(websocket.StatusNormalClosure, "bye")
		h.mu.Lock()
		current := h.conns[deviceID] == c
		if current {
			delete(h.conns, deviceID)
		}
		h.mu.Unlock()
		// A replaced connection must not mark the device offline while the
		// newer connection is still alive.
		if current {
			if err := h.store.SetDeviceOffline(context.Background(), deviceID); err != nil {
				h.log.Warn("mark device offline", "device", deviceID, "err", err)
			}
			h.log.Info("agent disconnected", "device", deviceID)
		}
	}()

	go h.writeLoop(ctx, c)
	go h.watchdog(ctx, c)

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText:
			h.handleFrame(ctx, c, data)
		case websocket.MessageBinary:
			h.log.Warn("unexpected binary frame", "device", deviceID, "bytes", len(data))
		}
	}
}

func (h *Hub) handleFrame(ctx context.Context, c *Conn, data []byte) {
	c.lastSeen.Store(time.Now().UnixNano())
	var frame model.AgentFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		h.log.Warn("bad agent frame", "device", c.DeviceID, "err", err)
		return
	}
	switch frame.Type {
	case "hello":
		if err := h.store.SetDeviceOnline(ctx, c.DeviceID, frame.Version, frame.OS, frame.Arch); err != nil {
			h.log.Warn("save agent hello", "device", c.DeviceID, "err", err)
		}
		_ = c.Send(model.AgentFrame{Type: "hello_ack", Version: "1", OK: true})
	case "heartbeat":
		if err := h.store.TouchDevice(ctx, c.DeviceID); err != nil {
			h.log.Warn("touch device", "device", c.DeviceID, "err", err)
		}
		_ = c.Send(model.AgentFrame{Type: "heartbeat_ack", TS: time.Now().Unix(), OK: true})
	case "config_request":
		h.mu.RLock()
		fn := h.onConfig
		h.mu.RUnlock()
		if fn != nil {
			go fn(c.DeviceID)
		}
	case "ack", "error":
		h.log.Info("agent frame", "device", c.DeviceID, "type", frame.Type, "ref", frame.Ref, "message", frame.Message)
	default:
		h.log.Debug("unhandled agent frame", "device", c.DeviceID, "type", frame.Type)
	}
}

func (h *Hub) writeLoop(ctx context.Context, c *Conn) {
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
				c.Close(websocket.StatusInternalError, "write failed")
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}

// watchdog closes a connection that stops sending heartbeats.
func (h *Hub) watchdog(ctx context.Context, c *Conn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := time.Unix(0, c.lastSeen.Load())
			if time.Since(last) > 90*time.Second {
				c.Close(websocket.StatusPolicyViolation, "heartbeat timeout")
				return
			}
		}
	}
}
