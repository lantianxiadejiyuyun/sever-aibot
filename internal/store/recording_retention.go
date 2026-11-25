package store

import (
	"context"
	"time"
)

// CleanupRecordings preserves access history and active sessions. Marking and
// removing payloads is atomic so clients can distinguish expiry from empty output.
func (s *Store) CleanupRecordings(ctx context.Context) (int64, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -settings.RecordingRetentionDays).Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET recording_purged_at=? WHERE kind='terminal' AND recording_purged_at='' AND ended_at IS NOT NULL AND julianday(ended_at)<julianday(?)`, now(), cutoff)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM access_recordings WHERE session_id IN (SELECT id FROM sessions WHERE recording_purged_at<>'')`); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
