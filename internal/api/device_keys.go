package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/store"
)

func (s *Server) authorizeDeviceKey(w http.ResponseWriter, r *http.Request, action, id string) bool {
	w.Header().Set("Cache-Control", "no-store")
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return false
	}
	if !store.StepUpValid(sess, time.Now()) {
		s.auditWithResult(r, action, id, nil, "denied")
		writeErr(w, http.StatusForbidden, "step_up_required", "请先重新验证管理员口令")
		return false
	}
	if !revealLimits.allow(sess.ID, time.Now()) {
		s.auditWithResult(r, action, id, nil, "denied")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "密钥操作过于频繁，请稍后再试")
		return false
	}
	return true
}

func (s *Server) deviceKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDeviceKeyUnavailable):
		writeErr(w, http.StatusConflict, "key_unavailable", "旧设备密钥仅保存了哈希，无法恢复；请保留现有密钥或轮换密钥")
	case errors.Is(err, store.ErrDeviceKeyMismatch):
		writeErr(w, http.StatusConflict, "key_mismatch", "设备密钥与当前密钥不匹配，请确认是否已轮换")
	case errors.Is(err, store.ErrNotFound):
		s.storeErr(w, err)
	default:
		writeErr(w, http.StatusInternalServerError, "device_key_failed", "设备密钥操作失败")
	}
}

func (s *Server) revealDeviceKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	const action = "device.reveal-key"
	if !s.authorizeDeviceKey(w, r, action, id) {
		return
	}
	v, ok := s.newVault(w)
	if !ok {
		s.auditWithResult(r, action, id, nil, "denied")
		return
	}
	key, err := s.store.RevealDeviceKey(r.Context(), v, id)
	if err != nil {
		s.auditWithResult(r, action, id, nil, "denied")
		s.deviceKeyError(w, err)
		return
	}
	defer clear(key)
	s.auditWithResult(r, action, id, nil, "ok")
	writeJSON(w, http.StatusOK, map[string]string{"deviceId": id, "deviceKey": string(key)})
}

func (s *Server) retainDeviceKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	const action = "device.retain-key"
	if !s.authorizeDeviceKey(w, r, action, id) {
		return
	}
	var req struct {
		DeviceKey string `json:"deviceKey"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	v, ok := s.newVault(w)
	if !ok {
		s.auditWithResult(r, action, id, nil, "denied")
		return
	}
	if err := s.store.RetainDeviceKey(r.Context(), v, id, req.DeviceKey); err != nil {
		s.auditWithResult(r, action, id, nil, "denied")
		s.deviceKeyError(w, err)
		return
	}
	s.auditWithResult(r, action, id, nil, "ok")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
