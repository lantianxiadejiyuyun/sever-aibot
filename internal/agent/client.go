// Package agent implements the outbound control-plane connection for a
// managed machine. It speaks JSON control frames over a WebSocket, sends
// heartbeats and reconnects with full-jitter exponential backoff.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"server-aibot/internal/access"
	"server-aibot/internal/model"
	"server-aibot/internal/relay"
)

// Version is the agent build version reported in the hello frame.
const Version = "0.1.0"

const (
	defaultHeartbeatInterval = 20 * time.Second
	defaultReadTimeout       = 90 * time.Second
	defaultConnectTimeout    = 30 * time.Second
	writeTimeout             = 10 * time.Second
	maxControlFrame          = 1 << 20
)

// ErrLiveness means no control frame arrived within the configured read
// timeout. The caller should reconnect.
var ErrLiveness = errors.New("agent: no control frame within read timeout")

// DialFunc opens the control-plane WebSocket. It matches websocket.Dial so
// tests can substitute an httptest-backed implementation.
type DialFunc func(ctx context.Context, endpoint string, header http.Header) (*websocket.Conn, *http.Response, error)

// Config configures a Client. Zero durations select defaults. DeviceKey is a
// secret: it is sent only in the Authorization header and is never logged.
type Config struct {
	ControlURL string
	DeviceID   string
	DeviceKey  string

	// Optional hello metadata; OS and Arch default to the running binary.
	Version string
	OS      string
	Arch    string

	// Relay is the dial policy for tunnel targets. A nil policy uses the
	// default private-network-only rules.
	Relay *relay.Policy

	// Optional tunables.
	HeartbeatInterval time.Duration
	ReadTimeout       time.Duration
	ConnectTimeout    time.Duration
	ReconnectBase     time.Duration
	ReconnectMax      time.Duration
	Dial              DialFunc
}

// Client maintains one logical agent session. It is safe to run once; Run
// owns the reconnect loop and expects no global state.
type Client struct {
	cfg           Config
	endpoint      string
	log           *slog.Logger
	dial          DialFunc
	state         *State
	now           func() time.Time
	jitter        func(time.Duration) time.Duration
	reconnectBase time.Duration
	reconnectMax  time.Duration
	tunnels       *tunnelManager
	metrics       *metricBuffer
}

// New validates cfg and constructs a Client. The device key is kept only in
// memory and is never included in logs or error values.
func New(cfg Config, log *slog.Logger) (*Client, error) {
	cfg.ControlURL = strings.TrimSpace(cfg.ControlURL)
	cfg.DeviceID = strings.TrimSpace(cfg.DeviceID)
	cfg.DeviceKey = strings.TrimSpace(cfg.DeviceKey)
	if cfg.ControlURL == "" || cfg.DeviceID == "" || cfg.DeviceKey == "" {
		return nil, errors.New("agent: control URL, device ID and device key are required")
	}
	endpoint, err := Endpoint(cfg.ControlURL)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if cfg.Version == "" {
		cfg.Version = Version
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaultConnectTimeout
	}
	if cfg.ReconnectBase <= 0 {
		cfg.ReconnectBase = defaultReconnectBase
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = defaultReconnectMax
	}
	if cfg.ReconnectMax < cfg.ReconnectBase {
		cfg.ReconnectMax = cfg.ReconnectBase
	}
	if cfg.Dial == nil {
		cfg.Dial = func(ctx context.Context, endpoint string, header http.Header) (*websocket.Conn, *http.Response, error) {
			return websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: header})
		}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &Client{
		cfg:           cfg,
		endpoint:      endpoint,
		log:           log,
		dial:          cfg.Dial,
		state:         &State{},
		now:           time.Now,
		jitter:        fullJitter,
		reconnectBase: cfg.ReconnectBase,
		reconnectMax:  cfg.ReconnectMax,
		tunnels:       newTunnelManager(cfg.DeviceID, cfg.Relay, log),
		metrics:       &metricBuffer{},
	}, nil
}

// State returns a snapshot of connection state. It never includes credentials.
func (c *Client) State() Snapshot { return c.state.snapshot() }

// Run connects and reconnects until ctx is cancelled. It returns nil on a
// clean shutdown and does not treat individual disconnects as fatal.
func (c *Client) Run(ctx context.Context) error {
	defer c.state.setStopped()
	accessURL, err := url.Parse(c.endpoint)
	if err != nil {
		return err
	}
	accessURL.Path, accessURL.RawPath, accessURL.RawQuery, accessURL.Fragment = "/agent/access", "", "", ""
	accessURL.ForceQuery = false
	policy := c.cfg.Relay
	if policy == nil {
		policy, err = relay.NewPolicy(relay.Config{})
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	metricsDone, metricsReady := make(chan struct{}), make(chan struct{})
	go func() { defer close(metricsDone); c.metrics.run(ctx, metricsReady) }()
	defer func() { cancel(); <-metricsDone }()
	<-metricsReady
	accessDone := make(chan struct{})
	go func() {
		defer close(accessDone)
		attempt := 0
		backoff := &Client{reconnectBase: c.reconnectBase, reconnectMax: c.reconnectMax, jitter: fullJitter}
		for ctx.Err() == nil {
			started := time.Now()
			_ = access.RunAgent(ctx, accessURL.String(), c.cfg.DeviceID, c.cfg.DeviceKey, policy)
			if time.Since(started) >= time.Minute {
				attempt = 0
			}
			// Use independent jitter: the main reconnect loop may use a test hook.
			delay := backoff.backoff(attempt)
			attempt++
			if sleepCtx(ctx, delay) != nil {
				return
			}
		}
	}()
	defer func() { cancel(); <-accessDone }()
	defer c.tunnels.Close()
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		c.state.beginConnect()
		acked, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if acked {
			// A successful hello_ack resets the exponential backoff.
			attempt = 0
		}
		delay := c.backoff(attempt)
		attempt++
		c.state.markReconnect(attempt, c.now().Add(delay))
		if err != nil {
			c.log.Warn("agent disconnected",
				"device", c.cfg.DeviceID,
				"endpoint", RedactURL(c.endpoint),
				"err", err,
				"reconnectIn", delay.Round(time.Millisecond),
			)
		} else {
			c.log.Warn("agent disconnected",
				"device", c.cfg.DeviceID,
				"endpoint", RedactURL(c.endpoint),
				"reconnectIn", delay.Round(time.Millisecond),
			)
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return nil
		}
	}
}

// connectOnce dials and runs a single session. acked reports whether the
// control plane accepted the hello, which resets the reconnect backoff.
func (c *Client) connectOnce(ctx context.Context) (acked bool, err error) {
	header := http.Header{}
	header.Set("X-Device-ID", c.cfg.DeviceID)
	header.Set("Authorization", "Bearer "+c.cfg.DeviceKey)
	header.Set("User-Agent", "server-aibot-agent/"+c.cfg.Version)

	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	conn, resp, err := c.dial(dialCtx, c.endpoint, header)
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		c.state.setDisconnected(err)
		return false, fmt.Errorf("connect: %w", err)
	}
	if conn == nil {
		err := errors.New("dial returned no connection")
		c.state.setDisconnected(err)
		return false, err
	}

	c.state.setConnected(c.now())
	c.log.Info("agent connected",
		"device", c.cfg.DeviceID,
		"endpoint", RedactURL(c.endpoint),
		"remote", RedactURL(c.cfg.ControlURL),
	)

	acked, err = c.runSession(ctx, conn)
	c.state.setDisconnected(err)
	return acked, err
}

func (c *Client) runSession(parent context.Context, conn *websocket.Conn) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() {
			_ = conn.CloseNow()
			cancel()
		})
	}

	var wg sync.WaitGroup
	defer func() {
		closeConn()
		wg.Wait()
	}()

	conn.SetReadLimit(maxControlFrame)

	var writeMu sync.Mutex
	hello := model.AgentFrame{
		Type:    "hello",
		Version: c.cfg.Version,
		OS:      c.cfg.OS,
		Arch:    c.cfg.Arch,
	}
	if err := c.writeFrame(ctx, conn, &writeMu, hello); err != nil {
		return false, fmt.Errorf("send hello: %w", err)
	}

	var acked atomic.Bool
	var timedOut atomic.Bool
	frames := make(chan struct{}, 1)
	configReqs := make(chan struct{}, 1)
	c.tunnels.SetNotify(func() {
		select {
		case configReqs <- struct{}{}:
		default:
		}
	})
	defer c.tunnels.SetNotify(nil)

	wg.Add(3)
	go func() {
		defer wg.Done()
		c.heartbeatLoop(ctx, conn, &writeMu, closeConn)
	}()
	go func() {
		defer wg.Done()
		c.watchdog(ctx, frames, &timedOut, closeConn)
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-configReqs:
				if err := c.writeFrame(ctx, conn, &writeMu, model.AgentFrame{Type: "config_request"}); err != nil {
					return
				}
			}
		}
	}()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if parent.Err() != nil {
				return acked.Load(), parent.Err()
			}
			if timedOut.Load() {
				return acked.Load(), ErrLiveness
			}
			if ctx.Err() != nil {
				return acked.Load(), errors.New("agent: session closed")
			}
			return acked.Load(), fmt.Errorf("read: %w", err)
		}
		c.state.markFrame(c.now())
		select {
		case frames <- struct{}{}:
		default:
		}
		if typ != websocket.MessageText {
			c.log.Debug("unexpected binary control frame", "device", c.cfg.DeviceID, "bytes", len(data))
			continue
		}
		var frame model.AgentFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			c.log.Warn("bad control frame", "device", c.cfg.DeviceID, "err", err)
			continue
		}
		switch frame.Type {
		case "hello_ack":
			if frame.OK {
				acked.Store(true)
				c.state.markHelloAck(c.now())
				c.log.Info("hello acknowledged", "device", c.cfg.DeviceID, "controlVersion", frame.Version)
				// Ask for tunnel config now; tickets are minted per request.
				if err := c.writeFrame(ctx, conn, &writeMu, model.AgentFrame{Type: "config_request"}); err != nil {
					c.log.Warn("config request failed", "device", c.cfg.DeviceID, "err", err)
				}
			} else {
				c.log.Warn("hello rejected", "device", c.cfg.DeviceID, "message", frame.Message)
			}
		case "config":
			c.tunnels.Apply(frame.Nodes)
			c.log.Info("tunnel config applied", "device", c.cfg.DeviceID, "nodes", len(frame.Nodes))
		case "heartbeat_ack":
			if frame.OK {
				c.metrics.ack(frame.MetricsAck)
			}
			c.log.Debug("heartbeat acknowledged", "device", c.cfg.DeviceID, "ts", frame.TS)
		default:
			c.log.Debug("control frame", "device", c.cfg.DeviceID, "type", frame.Type)
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, closeConn func()) {
	ticker := time.NewTicker(c.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			latest, batch := c.metrics.snapshot()
			frame := model.AgentFrame{Type: "heartbeat", TS: c.now().Unix(), Metrics: latest, MetricsBatch: batch}
			if err := c.writeFrame(ctx, conn, writeMu, frame); err != nil {
				c.log.Warn("heartbeat write failed", "device", c.cfg.DeviceID, "err", err)
				closeConn()
				return
			}
		}
	}
}

// watchdog closes a session when no control frame has arrived within
// ReadTimeout. Control pings every 30s and answers our 20s heartbeats, so a
// healthy connection always refreshes the timer.
func (c *Client) watchdog(ctx context.Context, frames <-chan struct{}, timedOut *atomic.Bool, closeConn func()) {
	timer := time.NewTimer(c.cfg.ReadTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-frames:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(c.cfg.ReadTimeout)
		case <-timer.C:
			timedOut.Store(true)
			c.log.Warn("agent liveness timeout",
				"device", c.cfg.DeviceID,
				"readTimeout", c.cfg.ReadTimeout,
			)
			closeConn()
			return
		}
	}
}

func (c *Client) writeFrame(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, frame model.AgentFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	writeMu.Lock()
	defer writeMu.Unlock()
	return conn.Write(writeCtx, websocket.MessageText, body)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
