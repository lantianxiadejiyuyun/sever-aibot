package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

// MountAccount belongs inside the authenticated /api/v1 router group.
func (s *Server) MountAccount(r chi.Router) {
	r.Get("/account/security", s.accountSecurity)
	r.With(s.requireCSRF).Post("/account/totp/setup", s.accountTOTPSetup)
	r.With(s.requireCSRF).Post("/account/totp/confirm", s.accountTOTPConfirm)
	r.With(s.requireCSRF).Post("/account/totp/recovery-codes", s.accountRecoveryCodes)
	r.With(s.requireCSRF).Post("/account/totp/disable", s.accountTOTPDisable)
	r.With(s.requireCSRF).Post("/account/"+"pass"+"word", s.accountPassword)
}

type accountRequest struct {
	Password    string `json:"password"`
	OTP         string `json:"otp"`
	NewPassword string `json:"newPassword"`
}

func (s *Server) accountVault() *vault.Vault {
	v, _ := vault.New(s.cfg.MasterKey)
	return v
}
func (s *Server) accountSecurity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, 401, "unauthenticated", "请先登录")
		return
	}
	state, err := s.store.AccountSecurity(r.Context(), sess.AdminID)
	if err != nil {
		s.internal(w, "读取账户安全状态失败", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, state)
}

// Every account mutation verifies the current password, independently of any
// previous step-up. Existing sessions alone cannot enroll or disable a factor.
func (s *Server) authorizeAccount(w http.ResponseWriter, r *http.Request, req *accountRequest) (*store.Admin, *store.Session, bool) {
	w.Header().Set("Cache-Control", "no-store")
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, 401, "unauthenticated", "请先登录")
		return nil, nil, false
	}
	if !s.allowLogin(clientIP(r)) {
		writeErr(w, 429, "rate_limited", "验证尝试过于频繁，请稍后再试")
		return nil, nil, false
	}
	if !decodeJSON(w, r, req) {
		return nil, nil, false
	}
	admin, err := s.store.Authenticate(r.Context(), sess.Username, req.Password)
	if err != nil || admin.ID != sess.AdminID {
		s.recordLoginFailure(clientIP(r))
		s.auditWithResult(r, "account.verify", sess.AdminID, nil, "denied")
		writeErr(w, 403, "invalid_credentials", "当前管理员密码不正确")
		return nil, nil, false
	}
	return admin, sess, true
}
func (s *Server) accountError(w http.ResponseWriter, r *http.Request, err error) {
	s.auditWithResult(r, "account.security", sessionFrom(r.Context()).AdminID, nil, "denied")
	switch {
	case errors.Is(err, store.ErrOTPRequired):
		writeErr(w, 403, "otp_required", "请输入验证器验证码或一次性恢复码")
	case errors.Is(err, store.ErrOTPInvalid):
		s.recordLoginFailure(clientIP(r))
		writeErr(w, 403, "invalid_otp", "验证码无效、已使用或已过期；请使用新的验证码或恢复码")
	case errors.Is(err, store.ErrTOTPEnabled):
		writeErr(w, 409, "totp_enabled", "双因素验证已开启")
	case errors.Is(err, store.ErrTOTPDisabled):
		writeErr(w, 409, "totp_disabled", "双因素验证尚未开启")
	case errors.Is(err, store.ErrTOTPSetupExpired):
		writeErr(w, 409, "totp_setup_expired", "绑定已过期，请重新开始")
	case errors.Is(err, store.ErrAccountChanged):
		writeErr(w, 403, "account_changed", "账户或会话已变更，请重新登录")
	case errors.Is(err, store.ErrAccountVault):
		writeErr(w, 503, "vault_unavailable", "双因素密钥服务不可用，请检查主密钥或使用恢复码")
	default:
		s.internal(w, "账户安全操作失败", err)
	}
}
func (s *Server) accountTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	admin, sess, ok := s.authorizeAccount(w, r, &req)
	if !ok {
		return
	}
	setup, err := s.store.BeginTOTP(r.Context(), admin, sess.ID, s.accountVault(), time.Now())
	if err != nil {
		s.accountError(w, r, err)
		return
	}
	s.audit(r, "account.totp.setup", admin.ID, nil)
	writeJSON(w, 200, setup)
}
func (s *Server) accountTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	admin, sess, ok := s.authorizeAccount(w, r, &req)
	if !ok {
		return
	}
	codes, err := s.store.ConfirmTOTP(r.Context(), admin, sess.ID, req.OTP, s.accountVault(), time.Now())
	if err != nil {
		s.accountError(w, r, err)
		return
	}
	s.resetLogin(clientIP(r))
	s.audit(r, "account.totp.enable", admin.ID, nil)
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}
func (s *Server) accountRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	s.accountUpdateTOTP(w, r, false)
}
func (s *Server) accountTOTPDisable(w http.ResponseWriter, r *http.Request) {
	s.accountUpdateTOTP(w, r, true)
}
func (s *Server) accountUpdateTOTP(w http.ResponseWriter, r *http.Request, disable bool) {
	var req accountRequest
	admin, sess, ok := s.authorizeAccount(w, r, &req)
	if !ok {
		return
	}
	codes, err := s.store.UpdateTOTP(r.Context(), admin, sess.ID, req.OTP, disable, s.accountVault(), time.Now())
	if err != nil {
		s.accountError(w, r, err)
		return
	}
	action := "account.totp.recovery_codes"
	if disable {
		action = "account.totp.disable"
	}
	s.resetLogin(clientIP(r))
	s.audit(r, action, admin.ID, nil)
	if disable {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	admin, sess, ok := s.authorizeAccount(w, r, &req)
	if !ok {
		return
	}
	if len(req.NewPassword) < 12 || len(req.NewPassword) > 1024 {
		writeErr(w, 400, "invalid_argument", "新密码长度必须为 12–1024 字节")
		return
	}
	if req.NewPassword == req.Password {
		writeErr(w, 400, "invalid_argument", "新密码必须与当前密码不同")
		return
	}
	if err := s.store.ChangeAccountPassword(r.Context(), admin, sess.ID, req.NewPassword, req.OTP, s.accountVault(), time.Now()); err != nil {
		s.accountError(w, r, err)
		return
	}
	s.resetLogin(clientIP(r))
	s.audit(r, "account.password.change", admin.ID, map[string]bool{"otherSessionsRevoked": true})
	writeJSON(w, 200, map[string]bool{"ok": true, "otherSessionsRevoked": true})
}

// accountLogin is the only password-login session issuer. Missing, invalid or
// replayed factors return before any cookie/session is created. The store commits
// factor consumption and session insertion atomically.
func (s *Server) accountLogin(w http.ResponseWriter, r *http.Request, admin *store.Admin, otp string) (*store.Session, string, bool) {
	sess, token, err := s.store.CreateAccountSession(r.Context(), admin, otp, clientIP(r), r.UserAgent(), time.Duration(s.cfg.SessionTTL)*time.Hour, s.accountVault(), time.Now())
	if err == nil {
		return sess, token, true
	}
	s.store.Audit(r.Context(), admin.Username, "auth.login", admin.ID, "", clientIP(r), r.UserAgent(), "denied")
	switch {
	case errors.Is(err, store.ErrOTPRequired):
		writeErr(w, 403, "otp_required", "请输入验证器验证码或一次性恢复码")
	case errors.Is(err, store.ErrOTPInvalid):
		s.recordLoginFailure(clientIP(r))
		writeErr(w, 401, "invalid_otp", "验证码无效、已使用或已过期")
	case errors.Is(err, store.ErrAccountChanged):
		s.recordLoginFailure(clientIP(r))
		writeErr(w, 401, "invalid_credentials", "账户已变更，请重新登录")
	case errors.Is(err, store.ErrAccountVault):
		writeErr(w, 503, "vault_unavailable", "双因素密钥服务不可用，可尝试一次性恢复码")
	default:
		s.internal(w, "创建会话失败", err)
	}
	return nil, "", false
}
