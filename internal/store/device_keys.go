package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"server-aibot/internal/vault"
)

const deviceKeyKind = "device_key"

var ErrDeviceKeyUnavailable = errors.New("device key unavailable")
var ErrDeviceKeyMismatch = errors.New("device key does not match current key")

func sealDeviceKey(id, key string, vaults []*vault.Vault) ([]byte, string, error) {
	if len(vaults) == 0 {
		return nil, "", nil
	}
	if len(vaults) != 1 || vaults[0] == nil {
		return nil, "", errors.New("device key vault unavailable")
	}
	plain := []byte(key)
	defer clear(plain)
	return vaults[0].Seal(id, deviceKeyKind, plain)
}

func (s *Store) RevealDeviceKey(ctx context.Context, v *vault.Vault, id string) ([]byte, error) {
	var sealed []byte
	var keyID, hash string
	err := s.db.QueryRowContext(ctx, `SELECT key_ct,key_id,key_hash FROM devices WHERE id=?`, id).Scan(&sealed, &keyID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(sealed) == 0 || keyID == "" {
		return nil, ErrDeviceKeyUnavailable
	}
	if v == nil {
		return nil, errors.New("device key vault unavailable")
	}
	plain, err := v.Open(id, deviceKeyKind, sealed, keyID)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash(string(plain))), []byte(hash)) != 1 {
		clear(plain)
		return nil, ErrDeviceKeyMismatch
	}
	return plain, nil
}

// RetainDeviceKey never changes authentication or runtime state. The hash predicate
// is compared by SQLite in the same atomic write as the ciphertext, so a concurrent
// rotation cannot attach an old secret to a new hash.
func (s *Store) RetainDeviceKey(ctx context.Context, v *vault.Vault, id, key string) error {
	hash, err := s.DeviceKeyHash(ctx, id)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash(key)), []byte(hash)) != 1 {
		return ErrDeviceKeyMismatch
	}
	sealed, keyID, err := sealDeviceKey(id, key, []*vault.Vault{v})
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET key_ct=?,key_id=? WHERE id=? AND key_hash=?`, sealed, keyID, id, hash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDeviceKeyMismatch
	}
	return nil
}
