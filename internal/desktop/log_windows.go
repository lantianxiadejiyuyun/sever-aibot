//go:build windows

package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// desktopLogHandler formats structured events for people, before buffer redaction.
// It intentionally omits device IDs, endpoints and other repetitive routing metadata.
type desktopLogHandler struct {
	buffer *logBuffer
	attrs  []slog.Attr
	group  string
}

func (h *desktopLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}
func (h *desktopLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}
func (h *desktopLogHandler) WithGroup(name string) slog.Handler {
	next := *h
	if name != "" {
		next.group += name + "."
	}
	return &next
}
func (h *desktopLogHandler) Handle(_ context.Context, record slog.Record) error {
	fields := map[string]slog.Value{}
	for _, attr := range h.attrs {
		fields[attr.Key] = attr.Value.Resolve()
	}
	record.Attrs(func(attr slog.Attr) bool { fields[attr.Key] = attr.Value.Resolve(); return true })
	get := func(key string) string {
		if v, ok := fields[key]; ok {
			return v.String()
		}
		return ""
	}
	message := record.Message
	switch message {
	case "agent connected":
		message = "已连接服务器，正在验证设备身份"
	case "hello acknowledged":
		message = "设备验证成功，已上线"
	case "agent disconnected":
		message = "连接中断"
		if reason := readableLogError(get("err")); reason != "" {
			message += "：" + reason
		}
		if delay, ok := fields["reconnectIn"]; ok && delay.Kind() == slog.KindDuration {
			seconds := max(1, int((delay.Duration()+time.Second-1)/time.Second))
			message += fmt.Sprintf("；%d 秒后自动重连", seconds)
		} else {
			message += "；将自动重连"
		}
	case "tunnel config applied":
		message = "转发配置已更新（节点数：" + get("nodes") + "）"
	case "tunnel connected":
		message = "转发通道已连接"
	case "tunnel disconnected":
		message = "转发通道断开，将自动重连"
	case "tunnel target dial failed":
		message = "无法访问目标 " + get("target")
	case "hello rejected":
		message = "设备验证失败，请检查设备 ID 和密钥"
	case "config request failed":
		message = "获取转发配置失败"
	case "bad control frame":
		message = "收到无法识别的服务器消息"
	case "heartbeat write failed":
		message = "发送心跳失败"
	case "agent liveness timeout":
		message = "服务器长时间未响应，准备重新连接"
	case "direct provider unavailable":
		message = "直连通道暂时不可用"
	}
	if record.Message != "agent disconnected" {
		if reason := readableLogError(get("err")); reason != "" {
			message += "：" + reason
		}
	}
	level := "信息"
	if record.Level >= slog.LevelError {
		level = "错误"
	} else if record.Level >= slog.LevelWarn {
		level = "提醒"
	}
	stamp := record.Time
	if stamp.IsZero() {
		stamp = time.Now()
	}
	_, err := h.buffer.Write([]byte(fmt.Sprintf("%s  [%s]\n%s\n\n", stamp.Local().Format("15:04:05"), level, singleLogLine(message))))
	return err
}
func singleLogLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
func readableLogError(text string) string {
	lower := strings.ToLower(text)
	switch {
	case text == "":
		return ""
	case strings.Contains(lower, "allowlisted port"):
		return "本机端口未放行，请调整“回环端口”或访问预设"
	case strings.Contains(lower, "destination denied"):
		return "目标地址未获允许，请检查访问权限"
	case strings.Contains(lower, "401"), strings.Contains(lower, "403"):
		return "认证被拒绝，请检查设备 ID 和密钥"
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "getaddrinfow"):
		return "无法解析服务器地址，请检查网络或地址"
	case strings.Contains(lower, "certificate"), strings.Contains(lower, "x509"):
		return "服务器证书验证失败，请检查系统时间和证书"
	case strings.Contains(lower, "refused"), strings.Contains(lower, "actively refused"):
		return "目标拒绝连接，请确认服务已启动"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline"), strings.Contains(lower, "did not properly respond"):
		return "连接超时，请检查网络或目标服务"
	case strings.Contains(lower, "reset"), strings.Contains(lower, "eof"), strings.Contains(lower, "closed"):
		return "连接已被关闭"
	case strings.Contains(lower, "503"):
		return "服务器暂时不可用"
	case strings.Contains(lower, "context canceled"):
		return "连接已取消"
	default:
		return singleLogLine(text)
	}
}
