package api

import (
	"net/http"
	"strings"
	"time"
)

type loginRequest struct {
	Username string `json:"username"`
	OTP      string `json:"otp"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.allowLogin(ip) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "invalid_credentials", "请输入用户名和密码")
		return
	}
	admin, err := s.store.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		s.recordLoginFailure(ip)
		// Do not store the attempted username: users sometimes paste a secret
		// into the wrong field, and audit logs must never hold credentials.
		s.store.Audit(r.Context(), "unknown", "auth.login", "", "", ip, r.UserAgent(), "denied")
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码不正确")
		return
	}
	sess, token, ok := s.accountLogin(w, r, admin, req.OTP)
	if !ok {
		return
	}
	s.resetLogin(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	s.store.Audit(r.Context(), admin.Username, "auth.login", "", "", ip, r.UserAgent(), "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]string{
			"id":       admin.ID,
			"username": admin.Username,
		},
		"csrfToken": sess.CSRF,
		"expiresAt": sess.ExpiresAt,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	clearSessionCookie(w, s.cfg.SecureCookie)
	if sess != nil {
		s.store.Audit(r.Context(), sess.Username, "auth.logout", "", "", clientIP(r), r.UserAgent(), "ok")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        map[string]string{"username": sess.Username},
		"csrfToken":   sess.CSRF,
		"expiresAt":   sess.ExpiresAt,
		"stepUpUntil": sess.StepUpUntil,
	})
}

const stepUpWindow = 5 * time.Minute

type stepUpRequest struct {
	Password string `json:"password"`
}

// stepUp re-authenticates the current session for sensitive reads such as
// credential reveal. The window is short and never extends the session itself.
func (s *Server) stepUp(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	ip := clientIP(r)
	if !s.allowLogin(ip) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "尝试过于频繁，请稍后再试")
		return
	}
	var req stepUpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := s.store.Authenticate(r.Context(), sess.Username, req.Password); err != nil {
		s.recordLoginFailure(ip)
		s.store.Audit(r.Context(), sess.Username, "auth.step-up", "", "", ip, r.UserAgent(), "denied")
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "管理员口令不正确")
		return
	}
	s.resetLogin(ip)
	until := time.Now().UTC().Add(stepUpWindow)
	if err := s.store.SetStepUp(r.Context(), sess.ID, until); err != nil {
		s.internal(w, "开启二次验证失败", err)
		return
	}
	sess.StepUpUntil = &until
	s.store.Audit(r.Context(), sess.Username, "auth.step-up", "", "", ip, r.UserAgent(), "ok")
	writeJSON(w, http.StatusOK, map[string]any{"stepUpUntil": until})
}

func (s *Server) allowLogin(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	a, ok := s.loginAttempts[ip]
	if !ok {
		return true
	}
	if time.Since(a.first) > 10*time.Minute {
		delete(s.loginAttempts, ip)
		return true
	}
	return a.count < 10
}

func (s *Server) recordLoginFailure(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	a := s.loginAttempts[ip]
	if a.first.IsZero() || time.Since(a.first) > 10*time.Minute {
		a = loginAttempt{first: time.Now()}
	}
	a.count++
	s.loginAttempts[ip] = a
}

func (s *Server) resetLogin(ip string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, ip)
	s.loginMu.Unlock()
}
