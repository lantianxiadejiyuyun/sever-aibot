package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"server-aibot/internal/access"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

type accessRuntime struct {
	broker *access.Broker
	mu     sync.Mutex
	live   map[string]*terminalRun
	init   sync.Once
}
type terminalRun struct {
	owner    string
	ctx      context.Context
	cancel   context.CancelFunc
	attached bool
	killed   atomic.Bool
	done     chan struct{}
}

var accessRuntimes sync.Map

func (s *Server) accessState() *accessRuntime {
	v, _ := accessRuntimes.LoadOrStore(s, &accessRuntime{broker: access.NewBroker(), live: map[string]*terminalRun{}})
	rt := v.(*accessRuntime)
	rt.init.Do(func() {
		if err := s.store.RecoverAccessSessions(context.Background()); err != nil {
			s.log.Error("recover access sessions", "err", err)
		}
	})
	return rt
}

// CloseAccess must be called after stopping new HTTP requests and before closing the store.
func (s *Server) CloseAccess() {
	value, ok := accessRuntimes.LoadAndDelete(s)
	if !ok {
		return
	}
	rt := value.(*accessRuntime)
	rt.mu.Lock()
	pending := make([]<-chan struct{}, 0, len(rt.live))
	for id, run := range rt.live {
		run.cancel()
		if !run.attached {
			_ = s.store.EndAccessSession(context.Background(), id, "interrupted", 0, 0)
			delete(rt.live, id)
			close(run.done)
		} else {
			pending = append(pending, run.done)
		}
	}
	rt.mu.Unlock()
	rt.broker.Close()
	for _, done := range pending {
		<-done
	}
}
func (s *Server) accessEngine() (*access.Engine, error) {
	v, err := vault.New(s.cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	return &access.Engine{Store: s.store, Vault: v, Broker: s.accessState().broker}, nil
}
func (s *Server) dialAsset(ctx context.Context, id string) (net.Conn, error) {
	e, err := s.accessEngine()
	if err != nil {
		return nil, err
	}
	c, _, err := e.Dial(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}
func (s *Server) MountAccess(r chi.Router) {
	r.With(s.requireCSRF).Post("/assets/{id}/hostkey/discover", s.discoverHostKey)
	r.With(s.requireCSRF).Post("/assets/{id}/hostkey/pin", s.pinHostKey)
	r.Get("/assets/{id}/checks", s.assetChecks)
	r.Get("/access/sessions", s.listAccessSessions)
	r.With(s.requireCSRF).Post("/access/sessions", s.createAccessSession)
	r.Get("/access/sessions/{id}/ws", s.terminalWS)
	r.With(s.requireCSRF).Post("/access/sessions/{id}/terminate", s.terminateAccessSession)
	r.Get("/access/sessions/{id}/recording", s.accessRecording)
}
func (s *Server) accessStepUp(w http.ResponseWriter, r *http.Request) bool {
	sess := sessionFrom(r.Context())
	if sess == nil || !store.StepUpValid(sess, time.Now()) {
		writeErr(w, 403, "step_up_required", "访问资产前请重新验证管理员口令")
		return false
	}
	return true
}
func (s *Server) accessError(w http.ResponseWriter, err error) {
	s.log.Warn("asset access failed", "err", err)
	writeErr(w, 409, "access_failed", err.Error())
}

// DisconnectAccessDevice revokes all streams immediately on key rotation/deletion.
func (s *Server) DisconnectAccessDevice(id string) { s.accessState().broker.Disconnect(id) }
func (s *Server) AgentAccessWS(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Device-ID")
	auth := r.Header.Get("Authorization")
	key := strings.TrimPrefix(auth, "Bearer ")
	want, err := s.store.DeviceKeyHash(r.Context(), id)
	if err != nil || !strings.HasPrefix(auth, "Bearer ") || key == "" || subtle.ConstantTimeCompare([]byte(want), []byte(store.TokenDigest(key))) != 1 {
		writeErr(w, 401, "unauthenticated", "设备凭据无效")
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Key rotation/deletion revokes established reverse channels too.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				hash, err := s.store.DeviceKeyHash(ctx, id)
				if err != nil || hash != want {
					cancel()
					return
				}
			}
		}
	}()
	_ = s.accessState().broker.Serve(ctx, id, websocket.NetConn(ctx, ws, websocket.MessageBinary))
}
func (s *Server) discoverHostKey(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	e, err := s.accessEngine()
	if err != nil {
		s.accessError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	o, err := e.Discover(ctx, chi.URLParam(r, "id"))
	if err != nil {
		s.accessError(w, err)
		return
	}
	s.audit(r, "asset.hostkey.discover", chi.URLParam(r, "id"), o)
	writeJSON(w, 200, o)
}
func (s *Server) pinHostKey(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	var req struct {
		Fingerprint string `json:"fingerprint"`
		Confirmed   bool   `json:"confirmed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Confirmed || req.Fingerprint == "" {
		writeErr(w, 400, "confirmation_required", "请明确确认观察到的主机指纹")
		return
	}
	a, err := s.store.GetAsset(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	e, err := s.accessEngine()
	if err != nil {
		s.accessError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	o, err := e.Discover(ctx, a.ID)
	if err != nil {
		s.accessError(w, err)
		return
	}
	if o.Fingerprint != req.Fingerprint {
		writeErr(w, 409, "hostkey_changed", "当前指纹与确认值不同，请重新检查")
		return
	}
	if err = s.store.PinAssetHostKey(ctx, a, o.Fingerprint); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "asset.hostkey.pin", a.ID, o)
	writeJSON(w, 200, o)
}
func (s *Server) assetChecks(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAssetChecks(r.Context(), chi.URLParam(r, "id"), 100)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) listAccessSessions(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAccessSessions(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) createAccessSession(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	var req struct {
		AssetID string `json:"assetId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.store.GetAsset(r.Context(), req.AssetID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !a.Enabled || a.Protocol != "ssh" || a.HostKey == "" {
		writeErr(w, 409, "asset_not_ready", "需要已启用、已确认主机指纹的 SSH 资产")
		return
	}
	rt := s.accessState()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.live) >= 32 {
		writeErr(w, 429, "session_limit", "终端会话数量已达上限")
		return
	}
	sess := sessionFrom(r.Context())
	row, err := s.store.CreateAccessSession(r.Context(), a.ID, sess.Username, sess.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &terminalRun{owner: sess.ID, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	rt.live[row.ID] = run
	go func() {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		rt.mu.Lock()
		if !run.attached && rt.live[row.ID] == run {
			delete(rt.live, row.ID)
			cancel()
			_ = s.store.EndAccessSession(context.Background(), row.ID, "expired", 0, 0)
			close(run.done)
		}
		rt.mu.Unlock()
	}()
	s.audit(r, "access.create", row.ID, map[string]string{"assetId": a.ID})
	writeJSON(w, 201, row)
}
func (s *Server) terminateAccessSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rt := s.accessState()
	rt.mu.Lock()
	run := rt.live[id]
	if run != nil {
		run.killed.Store(true)
		run.cancel()
		if !run.attached {
			delete(rt.live, id)
			_ = s.store.EndAccessSession(r.Context(), id, "killed", 0, 0)
			close(run.done)
		}
	}
	rt.mu.Unlock()
	if run == nil {
		writeErr(w, 404, "not_found", "会话不在运行")
		return
	}
	s.audit(r, "access.terminate", id, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) accessRecording(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	session, err := s.store.GetAccessSession(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if session.RecordingPurgedAt != "" {
		writeErr(w, http.StatusGone, "recording_expired", "录制已按保留策略清理，访问历史仍保留")
		return
	}
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Content-Disposition", "attachment; filename=terminal.cast")
	s.audit(r, "access.recording", id, nil)
	_ = s.store.WriteAccessRecording(r.Context(), id, w)
}
func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" || !s.originAllowed(origin.Host, r.Host) {
		writeErr(w, 403, "origin_rejected", "终端来源无效")
		return
	}
	id := chi.URLParam(r, "id")
	rt := s.accessState()
	rt.mu.Lock()
	run := rt.live[id]
	if run == nil || run.attached || run.owner != sessionFrom(r.Context()).ID {
		rt.mu.Unlock()
		writeErr(w, 409, "session_unavailable", "终端会话不可连接")
		return
	}
	run.attached = true
	rt.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
	stopRun := context.AfterFunc(run.ctx, cancel)
	defer stopRun()
	var in, out atomic.Int64
	result := "error"
	var cancelReason atomic.Value
	cancelReason.Store("disconnected")
	defer func() {
		cancel()
		run.cancel()
		if run.killed.Load() {
			result = "killed"
		}
		rt.mu.Lock()
		delete(rt.live, id)
		rt.mu.Unlock()
		_ = s.store.EndAccessSession(context.Background(), id, result, in.Load(), out.Load())
		s.audit(r, "access.end", id, map[string]string{"result": result})
		close(run.done)
	}()
	row, err := s.store.GetAccessSession(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{origin.Host}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 10)
	// First frame carries CSRF rather than putting it in a logged URL.
	authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
	_, raw, err := ws.Read(authCtx)
	authCancel()
	var hello struct {
		CSRF string `json:"csrf"`
	}
	if err != nil || json.Unmarshal(raw, &hello) != nil || subtle.ConstantTimeCompare([]byte(hello.CSRF), []byte(sessionFrom(r.Context()).CSRF)) != 1 {
		return
	}
	e, err := s.accessEngine()
	if err != nil {
		return
	}
	connectCtx, connectCancel := context.WithTimeout(ctx, 20*time.Second)
	client, chain, err := e.SSH(connectCtx, row.AssetID)
	connectCancel()
	if err != nil {
		_ = ws.Write(ctx, websocket.MessageText, []byte("SSH connection failed: "+err.Error()))
		return
	}
	defer chain.Close()
	defer client.Close()
	stopClient := context.AfterFunc(ctx, func() { client.Close(); chain.Close(); ws.CloseNow() })
	defer stopClient()
	setupTimer := time.AfterFunc(20*time.Second, func() { client.Close() })
	defer setupTimer.Stop()
	sshSession, err := client.NewSession()
	if err != nil {
		return
	}
	defer sshSession.Close()
	input, err := sshSession.StdinPipe()
	if err != nil {
		return
	}
	started := time.Now()
	var outputMu sync.Mutex
	writer := &terminalOutput{write: func(p []byte) (int, error) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if out.Load()+int64(len(p)) > 16<<20 {
			cancelReason.Store("output_limit")
			cancel()
			return 0, errors.New("recording limit reached")
		}
		if err := s.store.RecordAccessOutput(ctx, id, time.Since(started).Seconds(), p); err != nil {
			cancel()
			return 0, err
		}
		if err := ws.Write(ctx, websocket.MessageBinary, p); err != nil {
			return 0, err
		}
		out.Add(int64(len(p)))
		return len(p), nil
	}}
	sshSession.Stdout = writer
	sshSession.Stderr = writer
	if err = sshSession.RequestPty("xterm", 30, 120, nil); err != nil {
		return
	}
	if err = sshSession.Shell(); err != nil {
		return
	}
	setupTimer.Stop()
	stop := context.AfterFunc(ctx, func() { sshSession.Close(); client.Close(); ws.CloseNow() })
	defer stop()
	lastInput := atomic.Int64{}
	lastInput.Store(time.Now().Unix())
	cookie, _ := r.Cookie(sessionCookie)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Now().Unix()-lastInput.Load() > 900 {
					cancelReason.Store("timeout")
					cancel()
					return
				}
				if _, err := s.store.SessionByToken(ctx, cookie.Value); err != nil {
					cancelReason.Store("revoked")
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer cancel()
		var lastResize time.Time
		for {
			kind, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			lastInput.Store(time.Now().Unix())
			if kind == websocket.MessageBinary {
				if _, err = input.Write(data); err != nil {
					return
				}
				in.Add(int64(len(data)))
				continue
			}
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &msg) != nil {
				return
			}
			if msg.Type == "resize" && msg.Cols >= 1 && msg.Cols <= 500 && msg.Rows >= 1 && msg.Rows <= 200 {
				if time.Since(lastResize) < 100*time.Millisecond {
					continue
				}
				lastResize = time.Now()
				if sshSession.WindowChange(msg.Rows, msg.Cols) != nil {
					return
				}
				outputMu.Lock()
				recordErr := s.store.RecordAccessResize(ctx, id, time.Since(started).Seconds(), msg.Cols, msg.Rows)
				outputMu.Unlock()
				if recordErr != nil {
					return
				}
			} else if msg.Type == "close" {
				return
			}
		}
	}()
	err = sshSession.Wait()
	if ctx.Err() != nil {
		result = cancelReason.Load().(string)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result = "timeout"
		}
	} else if err == nil {
		result = "ok"
	}
	_ = ws.Close(websocket.StatusNormalClosure, "terminal ended")
}

type terminalOutput struct{ write func([]byte) (int, error) }

func (w *terminalOutput) Write(p []byte) (int, error) { return w.write(p) }

var _ io.Writer = (*terminalOutput)(nil)
