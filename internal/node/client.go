package node

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"server-aibot/internal/agent"
	"server-aibot/internal/model"
)

// Version is reported in the node hello frame.
const Version = "0.1.0"

const (
	heartbeatInterval = 20 * time.Second
	readTimeout       = 90 * time.Second
	connectTimeout    = 30 * time.Second
	writeTimeout      = 10 * time.Second
	maxControlFrame   = 1 << 20
	reconnectBase     = time.Second
	reconnectMax      = 60 * time.Second
)

// Config configures the node runtime.
type Config struct {
	ControlURL   string
	NodeID       string
	NodeKey      string
	StateDir     string
	TunnelListen string
	EntryHost    string
	TLSCert      string
	TLSKey       string
	// TunnelHTTP serves the tunnel as a WebSocket at /tunnel, so a fronting
	// reverse proxy (nginx) can terminate TLS for it.
	TunnelHTTP bool
}

// Runner owns the runtime and its control-plane connection loop.
type Runner struct {
	cfg      Config
	endpoint string
	log      *slog.Logger
	rt       *Runtime
}

// New validates the config and loads the optional tunnel TLS keypair.
func New(cfg Config, log *slog.Logger) (*Runner, error) {
	cfg.ControlURL = strings.TrimSpace(cfg.ControlURL)
	cfg.NodeID = strings.TrimSpace(cfg.NodeID)
	cfg.NodeKey = strings.TrimSpace(cfg.NodeKey)
	if cfg.ControlURL == "" || cfg.NodeID == "" || cfg.NodeKey == "" {
		return nil, errors.New("node: control URL, node ID and node key are required")
	}
	endpoint, err := agent.ControlEndpoint(cfg.ControlURL, "/node/ws")
	if err != nil {
		return nil, fmt.Errorf("node: %w", err)
	}
	var tlsConfig *tls.Config
	if cfg.TLSCert != "" {
		pair, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("node: load tunnel TLS keypair: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}
	return &Runner{
		cfg: cfg, endpoint: endpoint, log: log,
		rt: NewRuntime(cfg.NodeID, cfg.TunnelListen, cfg.EntryHost, tlsConfig, cfg.TunnelHTTP, log),
	}, nil
}

// Run starts the tunnel listener and reconnects to the control plane until ctx
// is cancelled. Data-plane tunnels survive control-plane restarts.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.rt.Start(ctx); err != nil {
		return err
	}
	defer r.rt.Close()

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		acked, err := r.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if acked {
			attempt = 0
		}
		delay := backoff(attempt)
		attempt++
		r.log.Warn("node disconnected",
			"node", r.cfg.NodeID,
			"endpoint", agent.RedactURL(r.endpoint),
			"err", err,
			"reconnectIn", delay.Round(time.Millisecond),
		)
		if sleep(ctx, delay) != nil {
			return nil
		}
	}
}

func (r *Runner) connectOnce(ctx context.Context) (acked bool, err error) {
	header := http.Header{}
	header.Set("X-Node-ID", r.cfg.NodeID)
	header.Set("Authorization", "Bearer "+r.cfg.NodeKey)
	header.Set("User-Agent", "server-aibot-node/"+Version)

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	conn, resp, err := websocket.Dial(dialCtx, r.endpoint, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxControlFrame)

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	go func() {
		<-sessionCtx.Done()
		_ = conn.CloseNow()
	}()
	var writeMu sync.Mutex
	write := func(frame model.AgentFrame) error {
		return writeFrame(sessionCtx, conn, &writeMu, frame)
	}
	r.rt.SetSender(write)
	defer r.rt.SetSender(nil)

	hello := model.AgentFrame{
		Type: "hello", Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		TLS: r.cfg.TLSCert != "",
	}
	if err := write(hello); err != nil {
		return false, fmt.Errorf("send hello: %w", err)
	}
	r.log.Info("node connected", "node", r.cfg.NodeID, "endpoint", agent.RedactURL(r.endpoint))

	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				if err := write(model.AgentFrame{Type: "heartbeat", TS: time.Now().Unix()}); err != nil {
					cancelSession()
					return
				}
			}
		}
	}()

	for {
		readCtx, cancelRead := context.WithTimeout(sessionCtx, readTimeout)
		typ, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			if ctx.Err() != nil {
				return acked, ctx.Err()
			}
			return acked, fmt.Errorf("read: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var frame model.AgentFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			r.log.Warn("bad control frame", "node", r.cfg.NodeID, "err", err)
			continue
		}
		switch frame.Type {
		case "hello_ack":
			if frame.OK {
				acked = true
				if err := write(model.AgentFrame{Type: "config_request"}); err != nil {
					return acked, fmt.Errorf("config request: %w", err)
				}
			} else {
				r.log.Warn("node hello rejected", "node", r.cfg.NodeID, "message", frame.Message)
			}
		case "config":
			r.rt.ApplyConfig(frame.TunnelSecret, frame.Forwards)
			r.log.Info("node config applied", "node", r.cfg.NodeID, "forwards", len(frame.Forwards))
		case "kick_device":
			r.rt.KickDevice(frame.DeviceID)
		case "heartbeat_ack":
			// Liveness only.
		default:
			r.log.Debug("node control frame", "node", r.cfg.NodeID, "type", frame.Type)
		}
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, mu *sync.Mutex, frame model.AgentFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	mu.Lock()
	defer mu.Unlock()
	return conn.Write(writeCtx, websocket.MessageText, body)
}

func backoff(attempt int) time.Duration {
	current := reconnectBase
	for i := 0; i < attempt && current < reconnectMax; i++ {
		current *= 2
	}
	if current > reconnectMax {
		current = reconnectMax
	}
	return time.Duration(rand.Int64N(int64(current) + 1))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
