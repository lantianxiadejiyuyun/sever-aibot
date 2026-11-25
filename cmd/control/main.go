// Command control runs the AI operations control plane, HTTP API and web
// console. It must run as a single process: SQLite and the scheduler assume
// one writer.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"server-aibot/internal/api"
	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/store"
)

func main() {
	log := newLogger()
	if err := run(log); err != nil {
		log.Error("control plane stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadControl()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	created, err := st.EnsureAdmin(context.Background(), cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		if strings.Contains(err.Error(), "at least 8") {
			return errors.New("first start requires AIBOT_ADMIN_PASSWORD (at least 8 characters); it is hashed and not stored")
		}
		return err
	}
	if created {
		log.Info("administrator created", "username", cfg.AdminUser)
	} else {
		log.Info("administrator exists; AIBOT_ADMIN_PASSWORD is ignored", "username", cfg.AdminUser)
	}
	if err := st.MarkAllDevicesOffline(context.Background()); err != nil {
		return err
	}
	if err := st.MarkAllNodesOffline(context.Background()); err != nil {
		return err
	}
	if err := st.CleanupSessions(context.Background()); err != nil {
		log.Warn("cleanup sessions", "err", err)
	}

	h := hub.New(st, log)
	srv := api.New(cfg, st, h, log)
	defer srv.CloseAccess()
	defer srv.StopOperations()
	defer srv.CloseChanges()
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: agent WebSockets and future SSE responses are long-lived.
		MaxHeaderBytes: 1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.StartOperations(ctx)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			if err := st.CleanupAudit(ctx); err != nil {
				log.Warn("cleanup audit", "err", err)
			}
			if count, err := st.CleanupRecordings(ctx); err != nil {
				log.Warn("cleanup recordings", "err", err)
			} else if count > 0 {
				log.Info("expired recordings removed", "sessions", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := st.CleanupSessions(ctx); err != nil {
					log.Warn("cleanup sessions", "err", err)
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	go func() {
		log.Info("control plane listening", "addr", cfg.Listen, "scheme", scheme, "db", cfg.DBPath)
		if cfg.TLSCert != "" {
			errCh <- httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
	return nil
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
