// Package api exposes the control-plane HTTP API and serves the embedded web
// console. All browser routes are same-origin; agent routes authenticate with
// a per-device key instead of a cookie.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/store"
	"server-aibot/web"
)

const sessionCookie = "aibot_session"

type Server struct {
	cfg   config.Control
	store *store.Store
	hub   *hub.Hub
	nodes *hub.Nodes
	log   *slog.Logger
	web   http.Handler

	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

type loginAttempt struct {
	count int
	first time.Time
}

func New(cfg config.Control, st *store.Store, h *hub.Hub, log *slog.Logger) *Server {
	s := &Server{
		cfg: cfg, store: st, hub: h, nodes: hub.NewNodes(st, log), log: log,
		web:           web.Handler(),
		loginAttempts: map[string]loginAttempt{},
	}
	// Push the current tunnel config as soon as either side reconnects, so a
	// control-plane restart converges without a manual apply.
	h.SetConnectHandler(func(deviceID string) {
		s.pushAgentConfig(context.Background(), deviceID)
	})
	h.SetConfigRequestHandler(func(deviceID string) {
		s.pushAgentConfig(context.Background(), deviceID)
	})
	s.nodes.SetConnectHandler(func(nodeID string) {
		ctx := context.Background()
		s.pushNodeConfig(ctx, nodeID)
		s.pushNodeForwardsToAgents(ctx, nodeID)
	})
	s.nodes.SetConfigRequestHandler(func(nodeID string) {
		s.pushNodeConfig(context.Background(), nodeID)
	})
	s.nodes.SetFrameHandler(s.handleNodeFrame)
	return s
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.accessLog)
	r.Use(s.securityHeaders)

	r.Get("/healthz", s.health)
	r.Handle("/agent/ws", http.HandlerFunc(s.agentWS))
	r.HandleFunc("/agent/access", s.AgentAccessWS)
	r.Handle("/node/ws", http.HandlerFunc(s.nodeWS))

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.With(s.requireCSRF).Post("/auth/logout", s.logout)
			r.Get("/auth/me", s.me)
			r.With(s.requireCSRF).Post("/auth/step-up", s.stepUp)

			r.Get("/overview", s.overview)
			r.Get("/audit", s.listAudit)

			r.Route("/devices", func(r chi.Router) {
				r.Get("/", s.listDevices)
				r.With(s.requireCSRF).Post("/", s.createDevice)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", s.getDevice)
					r.With(s.requireCSRF).Patch("/", s.updateDevice)
					r.With(s.requireCSRF).Delete("/", s.deleteDevice)
					r.With(s.requireCSRF).Post("/rotate-key", s.rotateDeviceKey)
					r.With(s.requireCSRF).Post("/reveal-key", s.revealDeviceKey)
					r.With(s.requireCSRF).Post("/retain-key", s.retainDeviceKey)
				})
			})

			r.Route("/nodes", func(r chi.Router) {
				r.Get("/", s.listNodes)
				r.With(s.requireCSRF).Post("/", s.createNode)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", s.getNode)
					r.With(s.requireCSRF).Patch("/", s.updateNode)
					r.With(s.requireCSRF).Delete("/", s.deleteNode)
					r.With(s.requireCSRF).Post("/rotate-key", s.rotateNodeKey)
				})
			})

			r.Route("/forwards", func(r chi.Router) {
				r.Get("/", s.listForwards)
				r.With(s.requireCSRF).Post("/", s.createForward)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", s.getForward)
					r.With(s.requireCSRF).Patch("/", s.updateForward)
					r.With(s.requireCSRF).Delete("/", s.deleteForward)
					r.With(s.requireCSRF).Post("/apply", s.applyForward)
				})
			})

			s.MountCredentials(r)
			s.MountAssets(r)
			s.MountSettings(r)

			s.MountMetrics(r)
			s.MountAccess(r)
			s.MountOperations(r)
			s.MountForwardRuntime(r)
			s.MountAI(r)
			s.MountDataSources(r)
			s.MountAccount(r)
			s.MountChanges(r)
		})
	})

	// Client-side routes fall back to the SPA; unknown API paths stay JSON.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "not_found", "接口不存在")
			return
		}
		s.web.ServeHTTP(w, r)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持该请求方法")
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	return r
}

// ---------- middleware ----------

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		// Never log query strings: agent keys are headers, but other secrets
		// may appear in URLs by mistake.
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "bytes", ww.BytesWritten(),
			"dur", time.Since(start).Round(time.Millisecond).String(), "ip", clientIP(r))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const sessionKey ctxKey = 1

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		sess, err := s.store.SessionByToken(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				clearSessionCookie(w, s.cfg.SecureCookie)
			}
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "会话已过期，请重新登录")
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess == nil {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
			return
		}
		got := r.Header.Get("X-CSRF-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
			writeErr(w, http.StatusForbidden, "csrf_failed", "CSRF 校验失败，请刷新页面")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !s.originAllowed(u.Host, r.Host) {
				writeErr(w, http.StatusForbidden, "origin_rejected", "请求来源不被允许")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed accepts the browser origin when it matches the Host this
// process sees or the configured public hostname. Behind nginx the request
// Host is the upstream address, so the public hostname must be configured.
func (s *Server) originAllowed(originHost, requestHost string) bool {
	if sameHostName(originHost, requestHost) {
		return true
	}
	return s.cfg.PublicHost != "" && sameHostName(originHost, s.cfg.PublicHost)
}

func sameHostName(a, b string) bool {
	return strings.EqualFold(dropDefaultPort(a), dropDefaultPort(b))
}

func dropDefaultPort(hostport string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(hostport))
	if err != nil {
		return strings.TrimSpace(hostport)
	}
	if port == "80" || port == "443" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func sessionFrom(ctx context.Context) *store.Session {
	sess, _ := ctx.Value(sessionKey).(*store.Session)
	return sess
}

// ---------- diagnostics ----------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dbOK := true
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		dbOK = false
		status = "degraded"
	}
	code := http.StatusOK
	if !dbOK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"status":       status,
		"db":           dbOK,
		"agentsOnline": s.hub.OnlineCount(),
		"nodesOnline":  s.nodes.OnlineCount(),
		"time":         time.Now().UTC(),
		"version":      "dev",
		"secureCookie": s.cfg.SecureCookie,
	})
}

func (s *Server) notImplemented(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotImplemented, "not_implemented", feature+" 将在后续里程碑提供")
	}
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	var body errBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", "请求体不是合法的 JSON")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func auditDetail(v any) string {
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(body) > 2000 {
		body = body[:2000]
	}
	return string(body)
}

func page[T any](items []T) map[string]any {
	return map[string]any{"items": items, "total": len(items)}
}
