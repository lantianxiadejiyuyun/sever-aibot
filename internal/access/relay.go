// Package access implements registered-asset SSH access and its agent transport.
package access

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"server-aibot/internal/relay"
)

type DialRequest struct {
	AssetID string `json:"assetId"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}
type Broker struct {
	mu     sync.Mutex
	agents map[string]*yamux.Session
}

func NewBroker() *Broker { return &Broker{agents: map[string]*yamux.Session{}} }
func (b *Broker) Disconnect(id string) {
	b.mu.Lock()
	s := b.agents[id]
	delete(b.agents, id)
	b.mu.Unlock()
	if s != nil {
		s.Close()
	}
}
func (b *Broker) Close() {
	b.mu.Lock()
	agents := b.agents
	b.agents = map[string]*yamux.Session{}
	b.mu.Unlock()
	for _, s := range agents {
		s.Close()
	}
}
func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.StreamOpenTimeout = 10 * time.Second
	return c
}
func (b *Broker) Serve(ctx context.Context, id string, c net.Conn) error {
	sess, err := yamux.Client(c, muxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()
	b.mu.Lock()
	old := b.agents[id]
	b.agents[id] = sess
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
	defer func() {
		b.mu.Lock()
		if b.agents[id] == sess {
			delete(b.agents, id)
		}
		b.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sess.CloseChan():
		return nil
	}
}
func (b *Broker) Dial(ctx context.Context, device string, req DialRequest) (net.Conn, error) {
	b.mu.Lock()
	s := b.agents[device]
	b.mu.Unlock()
	if s == nil {
		return nil, errors.New("agent access channel offline")
	}
	c, err := s.OpenStream()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err = json.NewEncoder(c).Encode(req); err != nil {
		c.Close()
		return nil, err
	}
	// A single-byte response avoids buffering any following SSH bytes.
	var status [1]byte
	if _, err = io.ReadFull(c, status[:]); err != nil {
		c.Close()
		return nil, err
	}
	if status[0] != 0 {
		c.Close()
		return nil, errors.New("agent rejected or failed destination dial")
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// RunAgent runs one authenticated reverse connection. The caller owns reconnect/backoff.
func RunAgent(ctx context.Context, endpoint, deviceID, key string, policy *relay.Policy) error {
	if policy == nil {
		return errors.New("access policy required")
	}
	h := http.Header{}
	h.Set("X-Device-ID", deviceID)
	h.Set("Authorization", "Bearer "+key)
	ws, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	sess, err := yamux.Server(websocket.NetConn(ctx, ws, websocket.MessageBinary), muxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	slots := make(chan struct{}, 32)
	for {
		c, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
			go func() { defer func() { <-slots }(); serveDial(ctx, c, policy) }()
		default:
			c.Close()
		}
	}
}
func serveDial(ctx context.Context, c net.Conn, p *relay.Policy) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	// Read only through newline, so no buffered bytes are lost at protocol switch.
	raw := make([]byte, 0, 512)
	var one [1]byte
	for len(raw) < 4096 {
		if _, err := io.ReadFull(c, one[:]); err != nil {
			return
		}
		if one[0] == '\n' {
			break
		}
		raw = append(raw, one[0])
	}
	var req DialRequest
	if json.Unmarshal(raw, &req) != nil || req.AssetID == "" {
		c.Write([]byte{1})
		return
	}
	upstream, err := p.DialContext(ctx, "tcp", net.JoinHostPort(req.Address, strconv.Itoa(req.Port)))
	if err != nil {
		c.Write([]byte{1})
		return
	}
	defer upstream.Close()
	if _, err = c.Write([]byte{0}); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() { io.Copy(upstream, c); upstream.Close(); close(done) }()
	io.Copy(c, upstream)
	c.Close()
	<-done
}
