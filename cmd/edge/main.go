// Command edge exposes the control console and the node tunnel on one TLS
// port by routing on the ClientHello server name. It does not terminate TLS:
// the original bytes go to the upstream, so the control plane and the node
// keep serving their own certificates.
//
// Configuration comes from the environment:
//
//	AIBOT_EDGE_LISTEN   listen address (default 0.0.0.0:443)
//	AIBOT_EDGE_ROUTES   name=host:port[,name=host:port...]
//	AIBOT_EDGE_DEFAULT  upstream for unknown names (empty refuses them)
//	AIBOT_LOG_LEVEL     debug, info, warn or error
//	AIBOT_LOG_FORMAT    text (default) or json
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"server-aibot/internal/config"
	"server-aibot/internal/edge"
)

func main() {
	log := newLogger()
	if err := run(log); err != nil {
		log.Error("edge stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadEdge()
	if err != nil {
		return err
	}
	routes, err := edge.ParseRoutes(cfg.Routes)
	if err != nil {
		return err
	}
	srv, err := edge.New(edge.Config{
		Listen:          cfg.Listen,
		Routes:          routes,
		DefaultUpstream: cfg.Default,
		Log:             log,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	log.Info("edge starting", "listen", cfg.Listen, "serverNames", strings.Join(names, ","), "default", cfg.Default)
	return srv.ListenAndServe(ctx)
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
