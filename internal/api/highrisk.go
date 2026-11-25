package api

import (
	"errors"
	"net/http"
	"time"

	"server-aibot/internal/store"
)

// RequireHighRiskFactor is called after the handler validates and authorizes
// its target, but before it persists approval or runs a high-risk action:
//
//	if !s.RequireHighRiskFactor(w, r, req.OTP) { return }
//
// The route must already use requireAuth and requireCSRF. OTP is supplied in
// the JSON body, never in the URL. This method reads no body, creates no grant,
// and does not mutate approval/execution state. Success consumes a TOTP even if
// the subsequent operation fails; a retry needs a new code. Recovery codes and
// single-factor step-up are not sufficient. Existing step-up is still required.
func (s *Server) RequireHighRiskFactor(w http.ResponseWriter, r *http.Request, otp string) bool {
	w.Header().Set("Cache-Control", "no-store")
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, 401, "unauthenticated", "请先登录")
		return false
	}
	if !s.allowLogin(clientIP(r)) {
		writeErr(w, 429, "rate_limited", "验证尝试过于频繁，请稍后再试")
		return false
	}
	err := s.store.ConsumeHighRiskTOTP(r.Context(), sess.AdminID, sess.ID, otp, s.accountVault(), time.Now())
	if err == nil {
		s.audit(r, "account.high_risk.verify", sess.AdminID, nil)
		return true
	}
	s.auditWithResult(r, "account.high_risk.verify", sess.AdminID, nil, "denied")
	switch {
	case errors.Is(err, store.ErrTOTPDisabled):
		writeErr(w, 403, "totp_required", "高风险操作必须使用 TOTP，请先在设置的账户安全中启用双因素验证")
	case errors.Is(err, store.ErrHighRiskStepUp):
		writeErr(w, 403, "step_up_required", "高风险操作前请先重新验证管理员口令")
	case errors.Is(err, store.ErrOTPRequired):
		writeErr(w, 403, "otp_required", "请输入验证器当前的 6 位 TOTP 验证码；恢复码不能用于高风险操作")
	case errors.Is(err, store.ErrOTPInvalid):
		s.recordLoginFailure(clientIP(r))
		writeErr(w, 403, "invalid_otp", "TOTP 验证码无效、已使用或已过期；请使用下一组验证码，恢复码不可用于高风险操作")
	case errors.Is(err, store.ErrAccountChanged):
		writeErr(w, 401, "unauthenticated", "账户或会话已失效，请重新登录")
	case errors.Is(err, store.ErrAccountVault):
		writeErr(w, 503, "vault_unavailable", "TOTP 密钥服务不可用，高风险操作已拒绝")
	default:
		s.internal(w, "验证高风险操作身份失败", err)
	}
	return false
}
