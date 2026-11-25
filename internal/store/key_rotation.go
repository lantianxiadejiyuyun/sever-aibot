package store

import (
	"context"
	"fmt"
	"server-aibot/internal/vault"
)

// RotateCredentialKey atomically re-encrypts all credentials. Call with the
// control process stopped; the caller installs the new environment key only
// after success. A wrong old key leaves every row unchanged.
func (s *Store) RotateCredentialKey(ctx context.Context, oldVault, newVault *vault.Vault) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,secret_ct,key_id FROM credentials`)
	if err != nil {
		return 0, err
	}
	type encrypted struct {
		id, kind, key string
		body          []byte
	}
	records := []encrypted{}
	for rows.Next() {
		var row encrypted
		if err = rows.Scan(&row.id, &row.kind, &row.body, &row.key); err != nil {
			rows.Close()
			return 0, err
		}
		records = append(records, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, r := range records {
		plain, e := oldVault.Open(r.id, r.kind, r.body, r.key)
		if e != nil {
			return 0, fmt.Errorf("credential %s cannot be decrypted", r.id)
		}
		sealed, key, e := newVault.Seal(r.id, r.kind, plain)
		clear(plain)
		if e != nil {
			return 0, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE credentials SET secret_ct=?,key_id=?,updated_at=? WHERE id=?`, sealed, key, now(), r.id); e != nil {
			return 0, e
		}
	}
	// Notification URLs are also encrypted under the same master key.
	rows, err = tx.QueryContext(ctx, `SELECT id,secret_ct,key_id FROM notification_channels`)
	if err != nil {
		return 0, err
	}
	channels := []encrypted{}
	for rows.Next() {
		var row encrypted
		row.kind = "webhook"
		if err = rows.Scan(&row.id, &row.body, &row.key); err != nil {
			rows.Close()
			return 0, err
		}
		channels = append(channels, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, r := range channels {
		plain, e := oldVault.Open(r.id, r.kind, r.body, r.key)
		if e != nil {
			return 0, fmt.Errorf("notification channel %s cannot be decrypted", r.id)
		}
		sealed, key, e := newVault.Seal(r.id, r.kind, plain)
		clear(plain)
		if e != nil {
			return 0, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE notification_channels SET secret_ct=?,key_id=? WHERE id=?`, sealed, key, r.id); e != nil {
			return 0, e
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,key_ct,key_id FROM devices WHERE key_ct IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	devices := []encrypted{}
	for rows.Next() {
		var row encrypted
		row.kind = deviceKeyKind
		if err = rows.Scan(&row.id, &row.body, &row.key); err != nil {
			rows.Close()
			return 0, err
		}
		devices = append(devices, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, r := range devices {
		plain, e := oldVault.Open(r.id, r.kind, r.body, r.key)
		if e != nil {
			return 0, fmt.Errorf("device %s cannot be decrypted", r.id)
		}
		sealed, key, e := newVault.Seal(r.id, r.kind, plain)
		clear(plain)
		if e != nil {
			return 0, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE devices SET key_ct=?,key_id=? WHERE id=?`, sealed, key, r.id); e != nil {
			return 0, e
		}
	}
	seedCount := 0
	for _, columns := range [][2]string{{"totp_seed_ct", "totp_key_id"}, {"pending_seed_ct", "pending_key_id"}} {
		// Column identifiers are a fixed allowlist, never caller input.
		rows, err = tx.QueryContext(ctx, `SELECT admin_id,`+columns[0]+`,`+columns[1]+` FROM admin_security WHERE `+columns[0]+` IS NOT NULL`)
		if err != nil {
			return 0, err
		}
		seeds := []encrypted{}
		for rows.Next() {
			var row encrypted
			row.kind = "admin_totp"
			if err = rows.Scan(&row.id, &row.body, &row.key); err != nil {
				rows.Close()
				return 0, err
			}
			seeds = append(seeds, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, err
		}
		for _, r := range seeds {
			plain, e := oldVault.Open(r.id, r.kind, r.body, r.key)
			if e != nil {
				return 0, fmt.Errorf("administrator authentication seed cannot be decrypted")
			}
			sealed, key, e := newVault.Seal(r.id, r.kind, plain)
			clear(plain)
			if e != nil {
				return 0, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE admin_security SET `+columns[0]+`=?,`+columns[1]+`=? WHERE admin_id=?`, sealed, key, r.id); e != nil {
				return 0, e
			}
			seedCount++
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(records) + len(channels) + len(devices) + seedCount, nil
}
