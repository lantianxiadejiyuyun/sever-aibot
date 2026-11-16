// Command agent connects one managed machine to the AI operations control
// plane.
//
// Configuration comes from the environment (never command-line flags):
//
//	AIBOT_CONTROL_URL  https://host, http://host, wss://host or ws://host
//	AIBOT_DEVICE_ID    device identifier created by the control plane
//	AIBOT_DEVICE_KEY    device key (required, never logged or written)
//	AIBOT_AGENT_STATE   state directory (default var/agent, mode 0700)
//	AIBOT_LOG_LEVEL     debug, info, warn or error
//	AIBOT_LOG_FORMAT    text (default) or json
//
// The agent sends a hello frame, heartbeats every 20s, reconnects with
// full-jitter backoff from 1s to 60s, and exits cleanly on SIGINT/SIGTERM.
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
	"server-aibot/internal/relay"
)

func main() {
	log := newLogger()
	if err := run(log); err != nil {
		log.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return err
	}
	if err := agent.EnsureStateDir(cfg.StateDir); err != nil {
		return err
	}

	policy, err := relay.NewPolicy(relay.Config{
		AllowCIDRs:         cfg.AllowCIDRs,
		AllowLoopbackPorts: cfg.AllowLoopbackPorts,
		AllowLinkLocal:     cfg.AllowLinkLocal,
	})
	if err != nil {
		return err
	}
	client, err := agent.New(agent.Config{
		ControlURL: cfg.ControlURL,
		DeviceID:   cfg.DeviceID,
		DeviceKey:  cfg.DeviceKey,
		Relay:      policy,
	}, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("agent starting",
		"device", cfg.DeviceID,
		"control", agent.RedactURL(cfg.ControlURL),
		"stateDir", cfg.StateDir,
		"version", agent.Version,
	)
	return client.Run(ctx)
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
