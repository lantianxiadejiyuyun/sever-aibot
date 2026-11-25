// Command node runs the public relay runtime on a cloud node. It connects
// outbound to the control plane, listens on assigned entry ports, and accepts
// tunnels from intranet agents.
//
// Configuration comes from the environment (never command-line flags):
//
//	AIBOT_CONTROL_URL         https://panel.example.com
//	AIBOT_NODE_ID             node identifier created by the control plane
//	AIBOT_NODE_KEY            one-time node key (never logged)
//	AIBOT_NODE_TUNNEL_LISTEN  tunnel listen address (default 0.0.0.0:7000)
//	AIBOT_NODE_TUNNEL_TRANSPORT tcp (default), kcp or quic; UDP requires TLS
//	AIBOT_NODE_RENDEZVOUS_LISTEN optional separate UDP rendezvous bind address
//	AIBOT_NODE_ENTRY_HOST     entry bind address (default 0.0.0.0)
//	AIBOT_NODE_TLS_CERT       optional tunnel TLS certificate (PEM)
//	AIBOT_NODE_TLS_KEY        optional tunnel TLS private key (PEM)
//	AIBOT_NODE_TUNNEL_HTTP    serve the tunnel as WebSocket /tunnel (set 1 when
//	                          nginx terminates TLS and proxies wss:// to it)
//	AIBOT_NODE_STATE          state directory (default var/node)
//	AIBOT_LOG_LEVEL           debug, info, warn or error
//	AIBOT_LOG_FORMAT          text (default) or json
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"server-aibot/internal/agent"
	"server-aibot/internal/config"
	"server-aibot/internal/node"
)

func main() {
	log := newLogger()
	if err := run(log); err != nil {
		log.Error("node stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadNode()
	if err != nil {
		return err
	}
	if err := agent.EnsureStateDir(cfg.StateDir); err != nil {
		return err
	}
	runner, err := node.New(node.Config{
		ControlURL:       cfg.ControlURL,
		NodeID:           cfg.NodeID,
		NodeKey:          cfg.NodeKey,
		StateDir:         cfg.StateDir,
		TunnelListen:     cfg.TunnelListen,
		TunnelTransport:  cfg.TunnelTransport,
		RendezvousListen: cfg.RendezvousListen,
		EntryHost:        cfg.EntryHost,
		TLSCert:          cfg.TLSCert,
		TLSKey:           cfg.TLSKey,
		TunnelHTTP:       cfg.TunnelHTTP,
	}, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	transport := cfg.TunnelTransport
	if cfg.TunnelHTTP {
		transport = "websocket"
	}
	log.Info("node starting",
		"node", cfg.NodeID,
		"control", agent.RedactURL(cfg.ControlURL),
		"tunnelListen", cfg.TunnelListen,
		"entryHost", cfg.EntryHost,
		"tls", cfg.TLSCert != "",
		"transport", transport,
	)
	return runner.Run(ctx)
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("AIBOT_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var handler slog.Handler
	if strings.EqualFold(os.Getenv("AIBOT_LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	return slog.New(handler)
}
