package store

import (
	"context"
	"time"
)

type Settings struct {
	SiteName               string `json:"siteName"`
	Announcement           string `json:"announcement"`
	AuditRetentionDays     int    `json:"auditRetentionDays"`
	RecordingRetentionDays int    `json:"recordingRetentionDays"`
	UpdatedAt              string `json:"updatedAt"`
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	var v Settings
	err := s.db.QueryRowContext(ctx, `SELECT site_name,announcement,audit_retention_days,recording_retention_days,updated_at FROM system_settings WHERE id=1`).Scan(&v.SiteName, &v.Announcement, &v.AuditRetentionDays, &v.RecordingRetentionDays, &v.UpdatedAt)
	return v, err
}
func (s *Store) SaveSettings(ctx context.Context, v Settings) error {
	_, err := s.db.ExecContext(ctx, `UPDATE system_settings SET site_name=?,announcement=?,audit_retention_days=?,recording_retention_days=CASE WHEN ?=0 THEN recording_retention_days ELSE ? END,updated_at=? WHERE id=1`, v.SiteName, v.Announcement, v.AuditRetentionDays, v.RecordingRetentionDays, v.RecordingRetentionDays, now())
	return err
}

type AdminSessionInfo struct {
	ID         string `json:"id"`
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	Current    bool   `json:"current"`
}

func (s *Store) ListAdminSessions(ctx context.Context, adminID, currentID string) ([]AdminSessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,ip,user_agent,created_at,last_seen_at,expires_at FROM admin_sessions WHERE admin_id=? AND expires_at>? ORDER BY last_seen_at DESC LIMIT 100`, adminID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminSessionInfo{}
	for rows.Next() {
		var v AdminSessionInfo
		if err := rows.Scan(&v.ID, &v.IP, &v.UserAgent, &v.CreatedAt, &v.LastSeenAt, &v.ExpiresAt); err != nil {
			return nil, err
		}
		v.Current = v.ID == currentID
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) RevokeAdminSession(ctx context.Context, adminID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE admin_id=? AND id=?`, adminID, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) CleanupAudit(ctx context.Context) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -settings.AuditRetentionDays).Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `DELETE FROM audit_logs WHERE created_at < ?`, cutoff)
	return err
}
