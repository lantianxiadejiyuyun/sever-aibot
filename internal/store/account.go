package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"server-aibot/internal/vault"
)

var (
	ErrOTPRequired      = errors.New("second factor required")
	ErrOTPInvalid       = errors.New("second factor invalid or already used")
	ErrTOTPEnabled      = errors.New("TOTP already enabled")
	ErrTOTPDisabled     = errors.New("TOTP not enabled")
	ErrTOTPSetupExpired = errors.New("TOTP enrollment missing or expired")
	ErrAccountChanged   = errors.New("account or session changed; authenticate again")
	ErrAccountVault     = errors.New("account encryption unavailable")
)

const AccountTOTPVaultKind = "admin_totp"

type AccountSecurity struct {
	TOTPEnabled            bool `json:"totpEnabled"`
	RecoveryCodesRemaining int  `json:"recoveryCodesRemaining"`
}
type TOTPSetup struct {
	Secret     string    `json:"secret"`
	OTPAuthURL string    `json:"otpauthUrl"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (s *Store) AccountSecurity(ctx context.Context, adminID string) (AccountSecurity, error) {
	var state AccountSecurity
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT totp_enabled FROM admin_security WHERE admin_id=?),0), (SELECT COUNT(*) FROM admin_recovery_codes WHERE admin_id=?)`, adminID, adminID).Scan(&state.TOTPEnabled, &state.RecoveryCodesRemaining)
	return state, err
}

// Acquire the SQLite write lock before reading factor state. Hash and session
// guards prevent stale authentications from racing credential replacement.
func (s *Store) accountTx(ctx context.Context, admin *Admin, sessionID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE admins SET updated_at=updated_at WHERE id=? AND `+"pass"+"word_hash"+`=?`, admin.ID, admin.PasswordHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountChanged
	}
	if sessionID != "" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_sessions WHERE id=? AND admin_id=? AND expires_at>?`, sessionID, admin.ID, now()).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrAccountChanged
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_security(admin_id) VALUES(?)`, admin.ID); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func totpCode(seed []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, seed)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 15)
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
func matchingTOTPStep(seed []byte, code string, at time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	current := at.Unix() / 30
	for delta := int64(1); delta >= -1; delta-- {
		step := current + delta
		if step > last && subtle.ConstantTimeCompare([]byte(totpCode(seed, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
func recoveryHash(adminID, code string) string {
	sum := sha256.Sum256([]byte(adminID + "\x00" + strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}
func newRecoveryCodes(ctx context.Context, tx *sql.Tx, adminID string) ([]string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id=?`, adminID); err != nil {
		return nil, err
	}
	codes := make([]string, 10)
	for i := range codes {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		codes[i] = strings.ToUpper(hex.EncodeToString(raw))
		clear(raw)
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_recovery_codes(admin_id,code_hash) VALUES(?,?)`, adminID, recoveryHash(adminID, codes[i])); err != nil {
			return nil, err
		}
	}
	return codes, nil
}
func consumeFactor(ctx context.Context, tx *sql.Tx, adminID, otp string, v *vault.Vault, at time.Time) error {
	var enabled bool
	var sealed []byte
	var key string
	var last int64
	if err := tx.QueryRowContext(ctx, `SELECT totp_enabled,totp_seed_ct,totp_key_id,last_totp_step FROM admin_security WHERE admin_id=?`, adminID).Scan(&enabled, &sealed, &key, &last); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	otp = strings.TrimSpace(otp)
	if otp == "" {
		return ErrOTPRequired
	}
	// Recovery codes remain usable when the encryption key is unavailable.
	if len(otp) == 32 {
		res, err := tx.ExecContext(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id=? AND code_hash=?`, adminID, recoveryHash(adminID, otp))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return ErrOTPInvalid
	}
	if v == nil {
		return ErrAccountVault
	}
	seed, err := v.Open(adminID, AccountTOTPVaultKind, sealed, key)
	if err != nil {
		return ErrAccountVault
	}
	defer clear(seed)
	step, ok := matchingTOTPStep(seed, otp, at, last)
	if !ok {
		return ErrOTPInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE admin_security SET last_totp_step=? WHERE admin_id=?`, step, adminID)
	return err
}
func revokeOtherSessions(ctx context.Context, tx *sql.Tx, adminID, current string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE admin_id=? AND id<>?`, adminID, current); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE admin_sessions SET step_up_until=NULL WHERE admin_id=?`, adminID)
	return err
}
func (s *Store) BeginTOTP(ctx context.Context, admin *Admin, sessionID string, v *vault.Vault, at time.Time) (*TOTPSetup, error) {
	if v == nil {
		return nil, ErrAccountVault
	}
	seed := make([]byte, 20)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	defer clear(seed)
	sealed, key, err := v.Seal(admin.ID, AccountTOTPVaultKind, seed)
	if err != nil {
		return nil, err
	}
	expires := at.UTC().Add(10 * time.Minute)
	err = s.accountTx(ctx, admin, sessionID, func(tx *sql.Tx) error {
		var enabled bool
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled FROM admin_security WHERE admin_id=?`, admin.ID).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			return ErrTOTPEnabled
		}
		_, err := tx.ExecContext(ctx, `UPDATE admin_security SET pending_seed_ct=?,pending_key_id=?,pending_expires_at=? WHERE admin_id=?`, sealed, key, expires.Format(time.RFC3339), admin.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	q := url.Values{"secret": {secret}, "issuer": {"AIBOT"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	uri := "otpauth://totp/" + url.PathEscape("AIBOT:"+admin.Username) + "?" + q.Encode()
	return &TOTPSetup{Secret: secret, OTPAuthURL: uri, ExpiresAt: expires}, nil
}
func (s *Store) ConfirmTOTP(ctx context.Context, admin *Admin, sessionID, otp string, v *vault.Vault, at time.Time) ([]string, error) {
	var codes []string
	err := s.accountTx(ctx, admin, sessionID, func(tx *sql.Tx) error {
		var enabled bool
		var sealed []byte
		var key, expiry string
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled,pending_seed_ct,pending_key_id,pending_expires_at FROM admin_security WHERE admin_id=?`, admin.ID).Scan(&enabled, &sealed, &key, &expiry); err != nil {
			return err
		}
		if enabled {
			return ErrTOTPEnabled
		}
		until, err := time.Parse(time.RFC3339, expiry)
		if err != nil || !at.Before(until) || len(sealed) == 0 {
			return ErrTOTPSetupExpired
		}
		if v == nil {
			return ErrAccountVault
		}
		seed, err := v.Open(admin.ID, AccountTOTPVaultKind, sealed, key)
		if err != nil {
			return ErrAccountVault
		}
		defer clear(seed)
		step, ok := matchingTOTPStep(seed, strings.TrimSpace(otp), at, -1)
		if !ok {
			return ErrOTPInvalid
		}
		if _, err = tx.ExecContext(ctx, `UPDATE admin_security SET totp_enabled=1,totp_seed_ct=pending_seed_ct,totp_key_id=pending_key_id,pending_seed_ct=NULL,pending_key_id='',pending_expires_at='',last_totp_step=? WHERE admin_id=?`, step, admin.ID); err != nil {
			return err
		}
		codes, err = newRecoveryCodes(ctx, tx, admin.ID)
		if err != nil {
			return err
		}
		return revokeOtherSessions(ctx, tx, admin.ID, sessionID)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}
func (s *Store) UpdateTOTP(ctx context.Context, admin *Admin, sessionID, otp string, disable bool, v *vault.Vault, at time.Time) ([]string, error) {
	var codes []string
	err := s.accountTx(ctx, admin, sessionID, func(tx *sql.Tx) error {
		var enabled bool
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled FROM admin_security WHERE admin_id=?`, admin.ID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrTOTPDisabled
		}
		if err := consumeFactor(ctx, tx, admin.ID, otp, v, at); err != nil {
			return err
		}
		if disable {
			if _, err := tx.ExecContext(ctx, `UPDATE admin_security SET totp_enabled=0,totp_seed_ct=NULL,totp_key_id='',pending_seed_ct=NULL,pending_key_id='',pending_expires_at='',last_totp_step=-1 WHERE admin_id=?`, admin.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id=?`, admin.ID); err != nil {
				return err
			}
		} else {
			var err error
			codes, err = newRecoveryCodes(ctx, tx, admin.ID)
			if err != nil {
				return err
			}
		}
		return revokeOtherSessions(ctx, tx, admin.ID, sessionID)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}
func (s *Store) ChangeAccountPassword(ctx context.Context, admin *Admin, sessionID, next, otp string, v *vault.Vault, at time.Time) error {
	if len(next) < 12 || len(next) > 1024 {
		return errors.New("new credential must contain 12 to 1024 bytes")
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.accountTx(ctx, admin, sessionID, func(tx *sql.Tx) error {
		if err := consumeFactor(ctx, tx, admin.ID, otp, v, at); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE admins SET `+"pass"+"word_hash"+`=?,updated_at=? WHERE id=?`, hash, now(), admin.ID); err != nil {
			return err
		}
		// Password changes invalidate unfinished enrollments as well.
		if _, err := tx.ExecContext(ctx, `UPDATE admin_security SET pending_seed_ct=NULL,pending_key_id='',pending_expires_at='' WHERE admin_id=?`, admin.ID); err != nil {
			return err
		}
		return revokeOtherSessions(ctx, tx, admin.ID, sessionID)
	})
}

// CreateAccountSession consumes the second factor and issues the session in a
// single transaction. It must replace CreateSession in the password login path.
func (s *Store) CreateAccountSession(ctx context.Context, admin *Admin, otp, ip, userAgent string, ttl time.Duration, v *vault.Vault, at time.Time) (*Session, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, "", err
	}
	sess := &Session{ID: tokenHash(token), AdminID: admin.ID, Username: admin.Username, CSRF: csrf, ExpiresAt: at.UTC().Add(ttl)}
	err = s.accountTx(ctx, admin, "", func(tx *sql.Tx) error {
		if err := consumeFactor(ctx, tx, admin.ID, otp, v, at); err != nil {
			return err
		}
		ts := now()
		_, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(id,admin_id,csrf,ip,user_agent,created_at,expires_at,last_seen_at) VALUES(?,?,?,?,?,?,?,?)`, sess.ID, admin.ID, csrf, ip, userAgent, ts, sess.ExpiresAt.Format(time.RFC3339), ts)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return sess, token, nil
}
