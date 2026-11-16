package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

// revealLimit is the documented reveal rate: five per minute per session.
const revealLimit = 5

// MountCredentials wires the credential vault routes. The caller mounts it
// inside the authenticated /api/v1 group.
func (s *Server) MountCredentials(r chi.Router) {
	r.Route("/credentials", func(r chi.Router) {
		r.Get("/", s.listCredentials)
		r.With(s.requireCSRF).Post("/", s.createCredential)
		r.Route("/{id}", func(r chi.Router) {
			r.With(s.requireCSRF).Patch("/", s.updateCredential)
			r.With(s.requireCSRF).Delete("/", s.deleteCredential)
			r.With(s.requireCSRF).Post("/reveal", s.revealCredential)
			r.With(s.requireCSRF).Post("/test", s.testCredential)
		})
	})
}

type credentialCreateRequest struct {
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Username   string          `json:"username"`
	Secret     string          `json:"secret"`
	PublicMeta json.RawMessage `json:"publicMeta"`
}

type credentialPatchRequest struct {
	Name       *string         `json:"name"`
	Username   *string         `json:"username"`
	Secret     *string         `json:"secret"`
	PublicMeta json.RawMessage `json:"publicMeta"`
}

func validCredentialKind(kind string) bool {
	switch kind {
	case "ssh_password", "ssh_key", "baota_api", "db_password", "api_token", "generic":
		return true
	}
	return false
}

// normalizePublicMeta accepts only a JSON object. An empty result means the
// caller did not send a usable value.
func normalizePublicMeta(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return "", errors.New("publicMeta 必须是 JSON 对象")
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func normalizePublicMetaPatch(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		empty := "{}"
		return &empty, nil
	}
	value, err := normalizePublicMeta(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Server) newVault(w http.ResponseWriter) (*vault.Vault, bool) {
	v, err := vault.New(s.cfg.MasterKey)
	if err != nil {
		s.log.Error("initialise vault", "err", err)
		writeErr(w, http.StatusInternalServerError, "vault_unavailable", "凭据加密服务不可用")
		return nil, false
	}
	return v, true
}

// ---------- list / create / update / delete ----------

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListCredentials(r.Context())
	if err != nil {
		s.internal(w, "读取凭据失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(items))
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var req credentialCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Kind = strings.TrimSpace(req.Kind)
	req.Username = strings.TrimSpace(req.Username)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "凭据名称不能为空")
		return
	}
	if !validCredentialKind(req.Kind) {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "凭据类型必须是 ssh_password/ssh_key/baota_api/db_password/api_token/generic")
		return
	}
	if req.Secret == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "凭据内容不能为空")
		return
	}
	publicMeta, err := normalizePublicMeta(req.PublicMeta)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if publicMeta == "" {
		publicMeta = "{}"
	}
	v, ok := s.newVault(w)
	if !ok {
		return
	}
	meta, err := s.store.CreateCredential(r.Context(), v, store.CredentialInput{
		Name: req.Name, Kind: req.Kind, Username: req.Username, Secret: req.Secret, PublicMeta: publicMeta,
	})
	if err != nil {
		s.internal(w, "创建凭据失败", err)
		return
	}
	s.audit(r, "credential.create", meta.ID, map[string]string{"name": meta.Name, "kind": meta.Kind})
	writeJSON(w, http.StatusCreated, meta)
}

func (s *Server) updateCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req credentialPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	patch := store.CredentialPatch{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "invalid_argument", "凭据名称不能为空")
			return
		}
		patch.Name = &name
	}
	if req.Username != nil {
		username := strings.TrimSpace(*req.Username)
		patch.Username = &username
	}
	if req.Secret != nil {
		if *req.Secret == "" {
			writeErr(w, http.StatusBadRequest, "invalid_argument", "凭据内容不能为空")
			return
		}
		patch.Secret = req.Secret
	}
	publicMeta, err := normalizePublicMetaPatch(req.PublicMeta)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	patch.PublicMeta = publicMeta

	v, ok := s.newVault(w)
	if !ok {
		return
	}
	meta, err := s.store.UpdateCredential(r.Context(), v, id, patch)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "credential.update", id, map[string]any{"secretRotated": patch.Secret != nil})
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteCredential(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrInUse) {
			writeErr(w, http.StatusConflict, "credential_in_use", "凭据仍被资产引用，请先解除引用")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "credential.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- reveal ----------

type revealBucket struct {
	window time.Time
	count  int
}

type revealLimiter struct {
	mu      sync.Mutex
	buckets map[string]*revealBucket
}

var revealLimits = &revealLimiter{buckets: map[string]*revealBucket{}}

// allow reserves one reveal slot, allowing five per rolling minute per key.
func (l *revealLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil || now.Sub(b.window) >= time.Minute {
		b = &revealBucket{window: now}
		l.buckets[key] = b
	}
	if b.count >= revealLimit {
		return false
	}
	b.count++
	return true
}

func (s *Server) revealCredential(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	id := chi.URLParam(r, "id")
	if !store.StepUpValid(sess, time.Now()) {
		s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "step_up_required"}, "denied")
		writeErr(w, http.StatusForbidden, "step_up_required", "查看凭据前请重新验证管理员口令")
		return
	}
	if !revealLimits.allow(sess.ID, time.Now()) {
		s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "rate_limited"}, "denied")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "凭据查看过于频繁，请稍后再试")
		return
	}
	v, ok := s.newVault(w)
	if !ok {
		s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "vault_unavailable"}, "denied")
		return
	}
	secret, err := s.store.RevealCredential(r.Context(), v, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "not_found"}, "denied")
			writeErr(w, http.StatusNotFound, "not_found", "凭据不存在")
			return
		}
		s.log.Error("reveal credential", "err", err)
		s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "decrypt_failed"}, "denied")
		writeErr(w, http.StatusInternalServerError, "decrypt_failed", "凭据解密失败，主密钥可能已轮换或数据已损坏")
		return
	}
	defer zeroBytes(secret.Plaintext)
	if err := s.store.TouchCredentialUsed(r.Context(), id); err != nil {
		s.log.Warn("touch credential", "err", err)
	}
	// Private keys are replace-only by default; exporting them would let a
	// compromised console impersonate the operator on every target.
	if secret.Kind == "ssh_key" {
		s.auditWithResult(r, "credential.reveal", id, map[string]string{"reason": "ssh_key_reveal_disabled"}, "denied")
		writeErr(w, http.StatusForbidden, "ssh_key_reveal_disabled", "SSH 私钥默认不允许导出，请使用替换凭据")
		return
	}
	s.auditWithResult(r, "credential.reveal", id, nil, "ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   secret.Username,
		"secret":     string(secret.Plaintext),
		"publicMeta": secret.PublicMeta,
	})
}

func (s *Server) testCredential(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "not_implemented", "凭据连通性测试将在中继就绪后提供")
}

// ---------- helpers ----------

// auditWithResult is the result-aware counterpart to s.audit (which always
// records "ok"). Detail must never contain secret material.
func (s *Server) auditWithResult(r *http.Request, action, target string, detail any, result string) {
	sess := sessionFrom(r.Context())
	actor := "system"
	if sess != nil {
		actor = sess.Username
	}
	s.store.Audit(r.Context(), actor, action, target, auditDetail(detail), clientIP(r), r.UserAgent(), result)
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
