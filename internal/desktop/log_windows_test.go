//go:build windows

package desktop

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestReadableDesktopLogs(t *testing.T) {
	buffer := &logBuffer{}
	buffer.setSecret("fake-sensitive-key")
	handler := &desktopLogHandler{buffer: buffer}
	at := time.Date(2026, 11, 23, 10, 20, 30, 0, time.Local)
	record := slog.NewRecord(at, slog.LevelWarn, "agent disconnected", 0)
	record.AddAttrs(slog.Any("err", errors.New("dial tcp: connection refused")), slog.Duration("reconnectIn", 1500*time.Millisecond), slog.String("device", "dev_noise"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	text := buffer.String()
	if !strings.Contains(text, "10:20:30  [提醒]\n连接中断：目标拒绝连接，请确认服务已启动；2 秒后自动重连") {
		t.Fatalf("unexpected output: %s", text)
	}
	if strings.Contains(text, "dev_noise") || strings.Contains(text, "level=") {
		t.Fatal("routing metadata leaked into display")
	}
	logger := slog.New(handler)
	logger.Warn("tunnel target dial failed", "target", "127.0.0.1:6768", "err", errors.New("relay: loopback requires an allowlisted port"))
	logger.With("err", errors.New("fake-sensitive-key\nforged log line")).Warn("unknown event")
	text = buffer.String()
	if !strings.Contains(text, "回环端口") || !strings.Contains(text, "127.0.0.1:6768") {
		t.Fatal("target guidance missing")
	}
	if strings.Contains(text, "fake-sensitive-key") || strings.Contains(text, "\nforged") {
		t.Fatal("secret or forged line exposed")
	}
	if handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug heartbeat noise enabled")
	}
}

func TestClearLogsRetainsRedaction(t *testing.T) {
	m := testManager(t)
	m.logs.setSecret("fake-key")
	m.logs.Write([]byte("old log\n"))
	m.ClearLogs()
	if m.Logs() != "" {
		t.Fatal("clear failed")
	}
	m.logs.Write([]byte("fake-key\n"))
	if strings.Contains(m.Logs(), "fake-key") {
		t.Fatal("clear removed redaction")
	}
}
