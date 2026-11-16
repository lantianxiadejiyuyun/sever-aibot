package store

import (
	"context"
	"time"
)

// SetStepUp marks a session as re-authenticated until the given time.
func (s *Store) SetStepUp(ctx context.Context, sessionID string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_sessions SET step_up_until=? WHERE id=?`,
		until.UTC().Format(time.RFC3339), sessionID)
	return err
}

// StepUpValid reports whether the session completed step-up and the window is
// still open. A nil session or window is invalid.
func StepUpValid(sess *Session, now time.Time) bool {
	return sess != nil && sess.StepUpUntil != nil && now.UTC().Before(*sess.StepUpUntil)
}
