package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"server-aibot/internal/asset"
	"server-aibot/internal/store"
)

// MountAssets wires the asset inventory routes. The caller mounts it inside
// the authenticated /api/v1 group.
func (s *Server) MountAssets(r chi.Router) {
	r.Route("/assets", func(r chi.Router) {
		r.Get("/", s.listAssets)
		r.With(s.requireCSRF).Post("/", s.createAsset)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.getAsset)
			r.With(s.requireCSRF).Patch("/", s.updateAsset)
			r.With(s.requireCSRF).Delete("/", s.deleteAsset)
			r.Get("/route", s.assetRoute)
			r.With(s.requireCSRF).Post("/check", s.checkAsset)
			r.Get("/ssh-config", s.assetSSHConfig)
		})
	})
}

type assetRequest struct {
	Name         *string `json:"name"`
	Address      *string `json:"address"`
	Port         *int    `json:"port"`
	Protocol     *string `json:"protocol"`
	ViaDeviceID  *string `json:"viaDeviceId"`
	ViaAssetID   *string `json:"viaAssetId"`
	CredentialID *string `json:"credentialId"`
	HostKey      *string `json:"hostKey"`
	Tags         *string `json:"tags"`
	Enabled      *bool   `json:"enabled"`
	Notes        *string `json:"notes"`
}

func applyAssetRequest(a *store.Asset, req assetRequest) {
	if req.Name != nil {
		a.Name = strings.TrimSpace(*req.Name)
	}
	if req.Address != nil {
		a.Address = strings.TrimSpace(*req.Address)
	}
	if req.Port != nil {
		a.Port = *req.Port
	}
	if req.Protocol != nil {
		a.Protocol = strings.TrimSpace(*req.Protocol)
	}
	if req.ViaDeviceID != nil {
		a.ViaDeviceID = strings.TrimSpace(*req.ViaDeviceID)
	}
	if req.ViaAssetID != nil {
		a.ViaAssetID = strings.TrimSpace(*req.ViaAssetID)
	}
	if req.CredentialID != nil {
		a.CredentialID = strings.TrimSpace(*req.CredentialID)
	}
	if req.HostKey != nil {
		a.HostKey = strings.TrimSpace(*req.HostKey)
	}
	if req.Tags != nil {
		a.Tags = strings.TrimSpace(*req.Tags)
	}
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if req.Notes != nil {
		a.Notes = *req.Notes
	}
}

func validAssetProtocol(protocol string) bool {
	switch protocol {
	case "ssh", "rdp", "vnc", "http", "mysql", "postgres":
		return true
	}
	return false
}

// validateAsset checks shape and references. The via chain is walked here so
// an update cannot introduce a cycle.
func (s *Server) validateAsset(r *http.Request, a *store.Asset) error {
	if a.Name == "" {
		return errors.New("资产名称不能为空")
	}
	if a.Address == "" {
		return errors.New("资产地址不能为空")
	}
	if a.Port < 1 || a.Port > 65535 {
		return errors.New("端口必须在 1-65535 之间")
	}
	if !validAssetProtocol(a.Protocol) {
		return errors.New("协议必须是 ssh/rdp/vnc/http/mysql/postgres")
	}
	if a.ViaAssetID != "" {
		if a.ViaAssetID == a.ID {
			return errors.New("跳板资产不能是资产自身")
		}
		seen := map[string]bool{}
		if a.ID != "" {
			seen[a.ID] = true
		}
		cur := a.ViaAssetID
		for i := 0; cur != ""; i++ {
			if i >= 64 {
				return errors.New("跳板链过长")
			}
			if seen[cur] {
				return errors.New("跳板链存在循环")
			}
			seen[cur] = true
			next, err := s.store.GetAsset(r.Context(), cur)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return errors.New("跳板资产不存在")
				}
				return errors.New("读取跳板资产失败")
			}
			cur = next.ViaAssetID
		}
	}
	if a.ViaDeviceID != "" {
		if _, err := s.store.GetDevice(r.Context(), a.ViaDeviceID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errors.New("跳板设备不存在")
			}
			return errors.New("读取跳板设备失败")
		}
	}
	if a.CredentialID != "" {
		if _, err := s.store.GetCredential(r.Context(), a.CredentialID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errors.New("凭据不存在")
			}
			return errors.New("读取凭据失败")
		}
	}
	return nil
}

// ---------- CRUD ----------

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.store.ListAssets(r.Context(), q.Get("device"), q.Get("tag"), q.Get("q"))
	if err != nil {
		s.internal(w, "读取资产失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(items))
}

func (s *Server) createAsset(w http.ResponseWriter, r *http.Request) {
	var req assetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := store.Asset{Port: 22, Protocol: "ssh", Enabled: true}
	applyAssetRequest(&a, req)
	if err := s.validateAsset(r, &a); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	created, err := s.store.CreateAsset(r.Context(), a)
	if err != nil {
		s.internal(w, "创建资产失败", err)
		return
	}
	s.audit(r, "asset.create", created.ID, map[string]any{
		"name": created.Name, "credentialId": created.CredentialID,
	})
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAsset(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) updateAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetAsset(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var req assetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	applyAssetRequest(existing, req)
	if err := s.validateAsset(r, existing); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	updated, err := s.store.UpdateAsset(r.Context(), *existing)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "asset.update", id, map[string]any{"credentialId": updated.CredentialID})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAsset(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrInUse) {
			writeErr(w, http.StatusConflict, "asset_in_use", "资产仍被其他资产作为跳板引用，请先解除引用")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "asset.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- route / check / ssh-config ----------

func (s *Server) assetRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	route, err := asset.Resolve(r.Context(), storeAssetLookup{store: s.store}, id)
	if err != nil {
		s.writeRouteErr(w, r, "asset.route", id, err)
		return
	}
	s.audit(r, "asset.route", id, map[string]any{"hops": route.HopIDs()})
	writeJSON(w, http.StatusOK, route)
}

func (s *Server) checkAsset(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "not_implemented", "资产连通性检查将在 Agent 中继就绪后提供")
}

func (s *Server) assetSSHConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := s.store.GetAsset(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	route, err := asset.Resolve(r.Context(), storeAssetLookup{store: s.store}, id)
	if err != nil {
		s.writeRouteErr(w, r, "asset.ssh-config", id, err)
		return
	}

	var b strings.Builder
	b.WriteString("# server-aibot generated OpenSSH client config\n")
	b.WriteString("# No passwords or private keys are included; provision your key at the IdentityFile paths.\n\n")
	for i, hop := range route.Hops {
		entryDevice := ""
		if i == 0 {
			entryDevice = route.DeviceID
		}
		writeSSHHost(&b, hop.ID, hop.Name, hop.Address, hop.Port, "", entryDevice)
		b.WriteString("\n")
	}
	proxyJump := make([]string, 0, len(route.Hops))
	for _, hop := range route.Hops {
		proxyJump = append(proxyJump, sshAlias(hop.ID, hop.Name))
	}
	entryDevice := ""
	if len(route.Hops) == 0 {
		entryDevice = route.DeviceID
	}
	writeSSHHost(&b, a.ID, a.Name, a.Address, a.Port, strings.Join(proxyJump, ","), entryDevice)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
	s.audit(r, "asset.ssh-config", id, map[string]any{
		"alias": sshAlias(a.ID, a.Name), "hops": route.HopIDs(),
	})
}

func (s *Server) writeRouteErr(w http.ResponseWriter, r *http.Request, action, id string, err error) {
	switch {
	case errors.Is(err, asset.ErrNotFound), errors.Is(err, asset.ErrCycle), errors.Is(err, asset.ErrTooManyHops):
		s.auditWithResult(r, action, id, map[string]string{"error": err.Error()}, "denied")
		writeErr(w, http.StatusBadRequest, "route_failed", err.Error())
	default:
		s.auditWithResult(r, action, id, map[string]string{"reason": "internal"}, "denied")
		s.internal(w, "解析资产路径失败", err)
	}
}

// storeAssetLookup adapts the store to asset.Lookup without exporting store
// types into the route package.
type storeAssetLookup struct{ store *store.Store }

func (l storeAssetLookup) GetAsset(ctx context.Context, id string) (asset.Node, error) {
	a, err := l.store.GetAsset(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return asset.Node{}, asset.ErrNotFound
		}
		return asset.Node{}, err
	}
	return asset.Node{
		ID: a.ID, Name: a.Name, Address: a.Address, Port: a.Port, Protocol: a.Protocol,
		ViaDeviceID: a.ViaDeviceID, ViaAssetID: a.ViaAssetID,
	}, nil
}

func writeSSHHost(b *strings.Builder, id, name, address string, port int, proxyJump, entryDevice string) {
	fmt.Fprintf(b, "Host %s\n", sshAlias(id, name))
	fmt.Fprintf(b, "    HostName %s\n", address)
	fmt.Fprintf(b, "    Port %d\n", port)
	fmt.Fprintf(b, "    IdentityFile ~/.ssh/server-aibot/%s\n", id)
	if proxyJump != "" {
		fmt.Fprintf(b, "    ProxyJump %s\n", proxyJump)
	}
	if entryDevice != "" {
		fmt.Fprintf(b, "    # entry via agent device %s (no public address)\n", entryDevice)
	}
}

// sshAlias builds a stable, shell-safe Host alias from the asset name and ID.
func sshAlias(id, name string) string {
	base := sanitizeSSHAlias(name)
	if base == "" {
		base = "asset"
	}
	suffix := sanitizeSSHAlias(id)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	if suffix == "" {
		suffix = "00000000"
	}
	return "aibot-" + base + "-" + suffix
}

func sanitizeSSHAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
