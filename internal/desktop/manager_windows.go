//go:build windows

// Package desktop hosts the existing Agent inside the Windows desktop app.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"server-aibot/internal/agent"
	"server-aibot/internal/relay"
)

type Config struct {
	ControlURL    string `json:"controlUrl"`
	DeviceID      string `json:"deviceId"`
	DeviceKey     string `json:"-"`
	AllowCIDRs    string `json:"allowCidrs"`
	LoopbackPorts string `json:"loopbackPorts"`
	AutoConnect   bool   `json:"autoConnect"`
	StartAtLogin  bool   `json:"startAtLogin"`
}

type savedConfig struct {
	Config
	Version      int    `json:"version"`
	ProtectedKey []byte `json:"protectedKey"`
}

type Status struct {
	Running, Connected bool
	Detail             string
	RetryAttempt       int
	NextRetryAt        time.Time
}

type Manager struct {
	// op serializes changes so a stopping Agent cannot overlap a new session.
	op      sync.Mutex
	mu      sync.Mutex
	dir     string
	client  *agent.Client
	cancel  context.CancelFunc
	done    chan struct{}
	logs    *logBuffer
	startup func(bool) (func() error, error)
}

func New() (*Manager, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return nil, errors.New("无法定位当前用户的 LocalAppData 目录")
	}
	return newManager(filepath.Join(base, "AIBOT", "Client"))
}

func newManager(dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建客户端目录: %w", err)
	}
	return &Manager{dir: dir, logs: &logBuffer{}, startup: setStartup}, nil
}
func (m *Manager) DataDir() string { return m.dir }
func (m *Manager) Load() (Config, error) {
	m.op.Lock()
	defer m.op.Unlock()
	return m.load()
}
func (m *Manager) load() (Config, error) {
	fallback := Config{ControlURL: "https://aibot.eugenweb.cn", AutoConnect: true}
	data, err := os.ReadFile(filepath.Join(m.dir, "config.json"))
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return fallback, fmt.Errorf("读取配置: %w", err)
	}
	var saved savedConfig
	if err = json.Unmarshal(data, &saved); err != nil {
		return fallback, errors.New("客户端配置格式损坏，请重新保存配置")
	}
	if saved.Version != 1 {
		return fallback, errors.New("客户端配置版本不支持")
	}
	key, err := unprotect(saved.ProtectedKey)
	if err != nil {
		return saved.Config, errors.New("无法解密设备密钥，请使用保存配置的 Windows 账户，或重新输入设备密钥")
	}
	saved.DeviceKey = string(key)
	clear(key)
	return saved.Config, nil
}

func validate(c Config) (*relay.Policy, error) {
	u, err := url.Parse(c.ControlURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "wss" && u.Scheme != "http" && u.Scheme != "ws") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("控制面地址须为 http(s)://主机 或 ws(s)://主机，不能包含凭据、查询参数或片段")
	}
	if strings.TrimSpace(c.DeviceID) == "" || strings.TrimSpace(c.DeviceKey) == "" {
		return nil, errors.New("请填写设备 ID 和设备密钥（不是管理员密码）")
	}
	if len(c.DeviceKey) > 8192 || len(c.DeviceID) > 256 {
		return nil, errors.New("设备 ID 或密钥过长")
	}
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	}
	var ports []int
	for _, s := range split(c.LoopbackPorts) {
		if s == "*" {
			for port := 1; port <= 65535; port++ {
				ports = append(ports, port)
			}
			continue
		}
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("回环端口须为 1–65535 的数字，使用逗号分隔，或填写 * 放行全部端口")
		}
		ports = append(ports, n)
	}
	var cidrs []string
	allowLinkLocal := false
	for _, cidr := range split(c.AllowCIDRs) {
		if cidr == "*" {
			cidrs = append(cidrs, "0.0.0.0/0", "::/0")
			allowLinkLocal = true
		} else {
			cidrs = append(cidrs, cidr)
		}
	}
	policy, err := relay.NewPolicy(relay.Config{AllowCIDRs: cidrs, AllowLoopbackPorts: ports, AllowLinkLocal: allowLinkLocal})
	if err != nil {
		return nil, errors.New("允许网段格式不正确，请填写 IP 或 CIDR，使用逗号分隔")
	}
	return policy, nil
}

func (m *Manager) Save(c Config) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	active := m.cancel != nil
	m.mu.Unlock()
	if active {
		return errors.New("请先断开连接，再保存配置")
	}
	c.ControlURL = strings.TrimSpace(c.ControlURL)
	c.DeviceID = strings.TrimSpace(c.DeviceID)
	c.DeviceKey = strings.TrimSpace(c.DeviceKey)
	if _, err := validate(c); err != nil {
		return err
	}
	encrypted, err := protect([]byte(c.DeviceKey))
	if err != nil {
		return fmt.Errorf("加密设备密钥失败: %w", err)
	}
	data, err := json.MarshalIndent(savedConfig{Config: c, Version: 1, ProtectedKey: encrypted}, "", "  ")
	if err != nil {
		return err
	}
	// Restore the previous startup registration if the config replacement fails.
	undo, err := m.startup(c.StartAtLogin)
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(m.dir, "config.json"), data); err != nil {
		if restoreErr := undo(); restoreErr != nil {
			return fmt.Errorf("保存失败: %v；恢复开机启动设置失败: %w", err, restoreErr)
		}
		return fmt.Errorf("保存配置: %w", err)
	}
	m.logs.setSecret(c.DeviceKey)
	return nil
}

func (m *Manager) Start() error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	active := m.cancel != nil
	m.mu.Unlock()
	if active {
		return nil
	}
	c, err := m.load()
	if err != nil {
		return err
	}
	policy, err := validate(c)
	if err != nil {
		return err
	}
	m.logs.setSecret(c.DeviceKey)
	client, err := agent.New(agent.Config{ControlURL: c.ControlURL, DeviceID: c.DeviceID, DeviceKey: c.DeviceKey, Relay: policy}, slog.New(&desktopLogHandler{buffer: m.logs}))
	if err != nil {
		return errors.New("无法初始化 Agent，请检查连接配置")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.client = client
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()
	go func() {
		if err := client.Run(ctx); err != nil {
			_, _ = m.logs.Write([]byte("Agent 已停止，请检查配置后重新连接。\n"))
		}
		cancel()
		m.mu.Lock()
		if m.client == client {
			m.cancel, m.client, m.done = nil, nil, nil
		}
		close(done)
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) Stop() {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	m.mu.Lock()
	m.cancel = nil
	m.client = nil
	m.done = nil
	m.mu.Unlock()
}
func (m *Manager) Close() { m.Stop() }
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil {
		return Status{Detail: "已断开"}
	}
	select {
	case <-m.done:
		return Status{Detail: "Agent 已停止，请断开后重试"}
	default:
	}
	state := m.client.State()
	if state.Connected && !state.LastHelloAckAt.IsZero() && !state.LastHelloAckAt.Before(state.LastConnectedAt) {
		return Status{Running: true, Connected: true, Detail: "已连接控制面"}
	}
	return connectionStatus(state, time.Now())
}

func connectionStatus(state agent.Snapshot, now time.Time) Status {
	detail := "正在连接控制面，失败后会自动重试。"
	if !state.NextRetryAt.IsZero() {
		seconds := max(0, int((state.NextRetryAt.Sub(now)+time.Second-1)/time.Second))
		detail = fmt.Sprintf("%d 秒后自动重连（第 %d 次）", seconds, state.RetryAttempt)
	} else if state.RetryAttempt > 0 {
		detail = fmt.Sprintf("正在自动重连（第 %d 次）", state.RetryAttempt)
	} else if state.Connected {
		detail = "正在等待控制面确认…"
	}
	return Status{Running: true, Detail: detail, RetryAttempt: state.RetryAttempt, NextRetryAt: state.NextRetryAt}
}
func (m *Manager) Logs() string { return m.logs.String() }
func (m *Manager) ClearLogs()   { m.logs.mu.Lock(); defer m.logs.mu.Unlock(); m.logs.text = "" }

type logBuffer struct {
	mu           sync.Mutex
	text, secret string
}

func (b *logBuffer) setSecret(s string) { b.mu.Lock(); defer b.mu.Unlock(); b.text = ""; b.secret = s }
func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := string(p)
	if b.secret != "" {
		s = strings.ReplaceAll(s, b.secret, "[已隐藏]")
		quoted := strconv.Quote(b.secret)
		s = strings.ReplaceAll(s, quoted[1:len(quoted)-1], "[已隐藏]")
	}
	b.text += s
	if len(b.text) > 64<<10 {
		b.text = b.text[len(b.text)-(64<<10):]
		if i := strings.IndexByte(b.text, '\n'); i >= 0 {
			b.text = b.text[i+1:]
		}
	}
	return len(p), nil
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.text }
