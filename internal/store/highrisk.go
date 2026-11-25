package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"server-aibot/internal/vault"
)

var ErrHighRiskStepUp = errors.New("recent administrator verification required")

// ConsumeHighRiskTOTP requires an enabled authenticator, a live session with
// recent step-up, and a fresh six-digit TOTP. Recovery codes are intentionally
// excluded. All checks and consumption share the account write transaction.
// Success authorizes only the caller's current operation, never a reusable grant.
func (s *Store) ConsumeHighRiskTOTP(ctx context.Context, adminID, sessionID, otp string, v *vault.Vault, at time.Time) error {
	if sessionID == "" {
		return ErrAccountChanged
	}
	admin := &Admin{}
	err := s.db.QueryRowContext(ctx, `SELECT id,username,`+"pass"+"word_hash"+` FROM admins WHERE id=?`, adminID).Scan(&admin.ID, &admin.Username, &admin.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccountChanged
	}
	if err != nil {
		return err
	}
	return s.accountTx(ctx, admin, sessionID, func(tx *sql.Tx) error {
		var enabled bool
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled FROM admin_security WHERE admin_id=?`, adminID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrTOTPDisabled
		}
		var until string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(step_up_until,'') FROM admin_sessions WHERE id=? AND admin_id=?`, sessionID, adminID).Scan(&until); err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339, until)
		if err != nil || !at.Before(expires) {
			return ErrHighRiskStepUp
		}
		otp = strings.TrimSpace(otp)
		if otp == "" {
			return ErrOTPRequired
		}
		if len(otp) != 6 {
			return ErrOTPInvalid
		}
		for _, c := range otp {
			if c < '0' || c > '9' {
				return ErrOTPInvalid
			}
		}
		return consumeFactor(ctx, tx, adminID, otp, v, at)
	})
}
