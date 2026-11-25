package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/store"
)

func (s *Server) MountSettings(r chi.Router) {
	r.Get("/settings", s.getSettings)
	r.With(s.requireCSRF).Put("/settings", s.putSettings)
	r.Get("/auth/sessions", s.adminSessions)
	r.With(s.requireCSRF).Delete("/auth/sessions/{id}", s.revokeAdminSession)
	r.Get("/audit/export", s.exportAudit)
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.Settings(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.Settings(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	v.SiteName = strings.TrimSpace(v.SiteName)
	if len(v.SiteName) == 0 || len(v.SiteName) > 100 || len(v.Announcement) > 4000 || v.AuditRetentionDays < 7 || v.AuditRetentionDays > 3650 || v.RecordingRetentionDays < 1 || v.RecordingRetentionDays > 3650 {
		writeErr(w, 400, "invalid_settings", "站点名称须为 1–100 字节，公告最多 4000 字节，审计保留期须为 7–3650 天，录制保留期须为 1–3650 天")
		return
	}
	if !store.StepUpValid(sessionFrom(r.Context()), time.Now()) {
		writeErr(w, 403, "step_up_required", "请先完成二次验证")
		return
	}
	if err := s.store.SaveSettings(r.Context(), v); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "settings.update", "system", map[string]any{"auditRetentionDays": v.AuditRetentionDays, "recordingRetentionDays": v.RecordingRetentionDays})
	s.getSettings(w, r)
}
func (s *Server) adminSessions(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	items, err := s.store.ListAdminSessions(r.Context(), sess.AdminID, sess.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}
func (s *Server) revokeAdminSession(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	id := chi.URLParam(r, "id")
	if !store.StepUpValid(sess, time.Now()) {
		writeErr(w, 403, "step_up_required", "请先完成二次验证")
		return
	}
	if err := s.store.RevokeAdminSession(r.Context(), sess.AdminID, id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "session.revoke", id, nil)
	if id == sess.ID {
		clearSessionCookie(w, s.cfg.SecureCookie)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// CSV cells are neutralized before export so user-controlled audit text cannot
// execute spreadsheet formulas when an administrator opens the file.
func csvCell(v string) string {
	trim := strings.TrimLeft(v, " \t\r\n")
	if len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + v
	}
	return v
}
func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAudit(r.Context(), 500)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"id", "time", "actor", "action", "target", "result", "ip", "detail"})
	for _, v := range items {
		row := []string{strconv.FormatInt(v.ID, 10), v.CreatedAt.UTC().Format(time.RFC3339), v.Actor, v.Action, v.Target, v.Result, v.IP, v.Detail}
		for i := range row {
			row[i] = csvCell(row[i])
		}
		if writer.Write(row) != nil {
			break
		}
	}
	writer.Flush()
	s.audit(r, "audit.export", "audit", map[string]any{"count": len(items)})
}
