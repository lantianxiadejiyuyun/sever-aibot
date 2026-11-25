// Package operations executes bounded inspections and durable notification deliveries.
package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

var ErrUnavailable = errors.New("目标访问层尚未接入，不能执行检查")
var ErrBusy = errors.New("检查正在运行或并发额度已满")

// Target is inventory-derived; callers cannot supply an arbitrary dial address.
type Target struct {
	Kind, ID, Host, DialHost, DeviceID, ViaAssetID string
	Port                                           int
}

// Dialer must honor context cancellation and preserve the target's registered
// access path (including agent and pinned SSH hops). No implicit fallback is allowed.
type Dialer interface {
	DialContext(context.Context, Target) (net.Conn, error)
}
type DialFunc func(context.Context, Target) (net.Conn, error)

func (f DialFunc) DialContext(ctx context.Context, t Target) (net.Conn, error) { return f(ctx, t) }

type Service struct {
	Store        *store.Store
	Vault        *vault.Vault
	Log          *slog.Logger
	mu           sync.RWMutex
	dialer       Dialer
	sshInspector SSHInspector
	closed       bool
	batchCancels map[string]context.CancelFunc
	slots        chan struct{}
	once         sync.Once
	lifecycle    sync.Mutex
	cancel       context.CancelFunc
	workers      sync.WaitGroup
}

func New(st *store.Store, v *vault.Vault, log *slog.Logger) *Service {
	return &Service{Store: st, Vault: v, Log: log, slots: make(chan struct{}, 4), batchCancels: map[string]context.CancelFunc{}}
}
func (s *Service) SetDialer(d Dialer) { s.mu.Lock(); defer s.mu.Unlock(); s.dialer = d }
func (s *Service) SetSSHInspector(inspect SSHInspector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sshInspector = inspect
}
func (s *Service) Start(ctx context.Context) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.closed {
		return
	}
	s.once.Do(func() {
		ctx, s.cancel = context.WithCancel(ctx)
		s.workers.Add(2)
		// Slow callbacks cannot hold up the inspection scheduler.
		for _, work := range []func(context.Context){s.tick, s.dispatch} {
			go func(work func(context.Context)) {
				defer s.workers.Done()
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					if ctx.Err() != nil {
						return
					}
					work(ctx)
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}(work)
		}
	})
}

// Close cancels scheduling and waits for owned workers to finish persisting results.
func (s *Service) Close() {
	s.lifecycle.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	for _, cancel := range s.batchCancels {
		cancel()
	}
	s.lifecycle.Unlock()
	s.workers.Wait()
}
func (s *Service) tick(ctx context.Context) {
	if err := s.Store.RecoverInspectionBatches(ctx); err != nil {
		s.logError("recover expired inspection batches", err)
	}
	checks, err := s.Store.ListOperationChecks(ctx)
	if err != nil {
		s.logError("list inspection schedule", err)
		return
	}
	for _, c := range checks {
		if !c.Enabled {
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			continue
		}
		claimed, err := s.Store.ClaimOperationCheck(ctx, c.ID, false)
		if err != nil {
			s.logError("claim inspection", err)
		}
		if !claimed {
			<-s.slots
			continue
		}
		s.workers.Add(1)
		go func(c store.OperationCheck) {
			defer s.workers.Done()
			defer func() { <-s.slots }()
			_, err := s.execute(ctx, c)
			if err != nil {
				s.logError("save inspection", err)
			}
		}(c)
	}
}
func (s *Service) dispatch(ctx context.Context) {
	deliveries, err := s.Store.DueNotificationOutbox(ctx)
	if err != nil {
		s.logError("list notification queue", err)
		return
	}
	for i, d := range deliveries {
		if i >= 10 || ctx.Err() != nil {
			break
		}
		claimed, err := s.Store.ClaimNotificationDelivery(ctx, d.ID)
		if err != nil {
			s.logError("claim notification", err)
		}
		if !claimed {
			continue
		}
		message := s.deliver(ctx, d)
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = s.Store.FinishNotificationDelivery(saveCtx, d, message)
		cancel()
		if err != nil {
			s.logError("save notification delivery", err)
		}
	}
}
func (s *Service) logError(message string, err error) {
	if s.Log != nil {
		s.Log.Error(message, "err", err)
	}
}
func (s *Service) Target(ctx context.Context, c store.OperationCheck) (Target, error) {
	if c.TargetKind == "asset" {
		a, err := s.Store.GetAsset(ctx, c.TargetID)
		if err != nil {
			return Target{}, err
		}
		if !a.Enabled {
			return Target{}, errors.New("资产已禁用")
		}
		return Target{Kind: "asset", ID: a.ID, Host: a.Address, Port: a.Port, DeviceID: a.ViaDeviceID, ViaAssetID: a.ViaAssetID}, nil
	}
	if c.TargetKind == "forward" {
		f, err := s.Store.GetForward(ctx, c.TargetID)
		if err != nil {
			return Target{}, err
		}
		if f.State != "applied" && f.State != "observed" {
			return Target{}, fmt.Errorf("%w: 转发尚未应用", ErrUnsupported)
		}
		n, err := s.Store.GetNode(ctx, f.NodeID)
		if err != nil {
			return Target{}, err
		}
		host := n.Addr
		if u, e := url.Parse(host); e == nil && u.Hostname() != "" {
			host = u.Hostname()
		} else if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		switch f.Kind {
		case "port":
			return Target{Kind: "forward", ID: f.ID, Host: host, DialHost: host, Port: f.RemotePort}, nil
		case "host":
			port := n.HTTPPort
			if port == 0 {
				port = 80
			}
			return Target{Kind: "forward", ID: f.ID, Host: f.Domain, DialHost: host, Port: port}, nil
		default:
			return Target{}, fmt.Errorf("%w: 该转发类型没有可供控制面探测的公网入口", ErrUnsupported)
		}
	}
	return Target{}, errors.New("targetKind 必须是 asset 或 forward")
}
func (s *Service) ValidateCheck(ctx context.Context, c store.OperationCheck) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 120 {
		return errors.New("名称不能为空且最多 120 字节")
	}
	if c.Kind != "tcp" && c.Kind != "http" && c.Kind != "metric" && c.Kind != "ssh" {
		return errors.New("kind 必须是 tcp、http、metric 或 ssh")
	}
	if c.TimeoutMS < 100 || c.TimeoutMS > 30000 {
		return errors.New("timeoutMs 必须在 100 到 30000 之间")
	}
	if c.IntervalSeconds < 30 || c.IntervalSeconds > 86400 {
		return errors.New("intervalSeconds 必须在 30 到 86400 之间")
	}
	if c.Kind == "ssh" {
		if c.TargetKind != "asset" {
			return errors.New("SSH 巡检必须选择资产")
		}
		if _, ok := sshProfiles[c.Profile]; !ok {
			return errors.New("profile 必须是 system、services、docker 或 firewall")
		}
		a, err := s.Store.GetAsset(ctx, c.TargetID)
		if err != nil {
			return err
		}
		if !a.Enabled || a.Protocol != "ssh" || a.HostKey == "" || a.CredentialID == "" {
			return errors.New("SSH 资产必须启用、绑定凭据并固定主机指纹")
		}
		return nil
	}
	if c.Kind == "metric" {
		if c.TargetKind != "device" {
			return errors.New("指标检查必须选择登记设备")
		}
		if c.Metric != "cpu" && c.Metric != "memory" && c.Metric != "load" && c.Metric != "root_disk" {
			return errors.New("metric 必须是 cpu、memory、root_disk 或 load")
		}
		if math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.Threshold <= 0 || (c.Metric != "load" && c.Threshold > 100) {
			return errors.New("阈值必须大于 0；CPU 和内存百分比不能超过 100")
		}
		if c.MaxAgeSeconds < 60 || c.MaxAgeSeconds > 3600 {
			return errors.New("指标有效期必须在 60 到 3600 秒之间")
		}
		_, err := s.Store.GetDevice(ctx, c.TargetID)
		return err
	}
	if c.TargetKind != "asset" && c.TargetKind != "forward" {
		return errors.New("连接检查必须选择资产或转发")
	}
	if c.Kind == "http" {
		if c.Scheme != "http" && c.Scheme != "https" {
			return errors.New("scheme 必须是 http 或 https")
		}
		u, err := url.Parse(c.Path)
		if err != nil || !strings.HasPrefix(c.Path, "/") || strings.HasPrefix(c.Path, "//") || u.IsAbs() || u.Host != "" || u.Fragment != "" || len(c.Path) > 2048 {
			return errors.New("path 必须是站内绝对路径，不能包含主机或 fragment")
		}
	}
	t, err := s.Target(ctx, c)
	if err != nil {
		return err
	}
	if t.Port < 1 || t.Port > 65535 || strings.TrimSpace(t.Host) == "" {
		return errors.New("登记目标地址或端口无效")
	}
	return nil
}
func (s *Service) Run(ctx context.Context, id string) (store.InspectionRun, error) {
	c, err := s.Store.GetOperationCheck(ctx, id)
	if err != nil {
		return store.InspectionRun{}, err
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return store.InspectionRun{}, ErrBusy
	}
	defer func() { <-s.slots }()
	ok, err := s.Store.ClaimOperationCheck(ctx, id, true)
	if err != nil {
		return store.InspectionRun{}, err
	}
	if !ok {
		return store.InspectionRun{}, ErrBusy
	}
	return s.execute(ctx, c)
}
func (s *Service) execute(ctx context.Context, c store.OperationCheck) (store.InspectionRun, error) {
	// Refresh after acquiring the lease: an editor may have saved between the
	// scheduler's list and its claim. Leased definitions cannot be edited.
	fresh, err := s.Store.GetOperationCheck(ctx, c.ID)
	if err != nil {
		return store.InspectionRun{}, err
	}
	c = fresh
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	state, message := "up", "检查成功"
	detail := store.InspectionDetail{Source: "control"}
	t, err := s.Target(probeCtx, c)
	if c.Kind == "ssh" {
		s.mu.RLock()
		inspect := s.sshInspector
		s.mu.RUnlock()
		if inspect == nil {
			state, message = "stale", ErrUnavailable.Error()
		} else {
			state, message, detail = inspect(probeCtx, c)
		}
	} else if c.Kind == "metric" {
		detail.Source = "agent"
		state, message = s.metric(probeCtx, c)
	} else if err != nil {
		state = "stale"
		message = "登记目标不可用"
		if errors.Is(err, ErrUnsupported) {
			message = err.Error()
		}
	} else {
		if t.Kind == "asset" {
			detail.Source = "asset-route"
		}
		s.mu.RLock()
		dialer := s.dialer
		s.mu.RUnlock()
		if dialer == nil {
			err = ErrUnavailable
		} else {
			err = probe(probeCtx, dialer, t, c)
		}
		if err != nil {
			state = "down"
			message = "连接失败、超时或 HTTP 响应不符合要求"
			if errors.Is(err, ErrUnsupported) {
				state = "stale"
				message = err.Error()
			}
			if errors.Is(err, ErrUnavailable) {
				state = "stale"
				message = ErrUnavailable.Error()
			}
		}
	}
	// Persist even when a caller disconnects; a finished probe must release its lease.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	return s.Store.FinishOperationCheckDetail(saveCtx, c, state, message, time.Since(start).Milliseconds(), detail)
}
func probe(ctx context.Context, d Dialer, t Target, c store.OperationCheck) error {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, t) }
	if c.Kind == "tcp" {
		conn, err := dial(ctx, "tcp", "")
		if err != nil {
			return err
		}
		return conn.Close()
	}
	transport := &http.Transport{DialContext: dial, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10, TLSHandshakeTimeout: time.Duration(c.TimeoutMS) * time.Millisecond}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}
	u, err := url.Parse(c.Scheme + "://" + net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) + c.Path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if n > 1<<20 {
		return errors.New("response too large")
	}
	return nil
}
