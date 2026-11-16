package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"server-aibot/internal/vault"
)

// ErrInUse is returned when a delete would orphan rows that still reference
// the target record. Callers should surface this as HTTP 409.
var ErrInUse = errors.New("record is referenced by another record")

// CredentialMeta is the non-sensitive projection returned by list/get.
type CredentialMeta struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Username   string         `json:"username"`
	PublicMeta map[string]any `json:"publicMeta"`
	KeyID      string         `json:"keyId"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	LastUsedAt *time.Time     `json:"lastUsedAt,omitempty"`
}

// CredentialInput is a create request. Secret is plaintext and is encrypted
// before it reaches SQLite.
type CredentialInput struct {
	Name       string
	Kind       string
	Username   string
	Secret     string
	PublicMeta string
}

// CredentialPatch updates metadata and optionally replaces the secret.
type CredentialPatch struct {
	Name       *string
	Username   *string
	Secret     *string
	PublicMeta *string
}

// CredentialSecret is the decrypted reveal payload. Callers must avoid logging
// or persisting Plaintext.
type CredentialSecret struct {
	ID         string
	Kind       string
	Username   string
	Plaintext  []byte
	PublicMeta map[string]any
}

const credentialMetaColumns = `id,name,kind,COALESCE(username,''),public_meta,key_id,created_at,updated_at,last_used_at`

func newCredentialID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "cred_" + hex.EncodeToString(b)
}

func (s *Store) ListCredentials(ctx context.Context) ([]CredentialMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+credentialMetaColumns+` FROM credentials ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialMeta{}
	for rows.Next() {
		m, err := scanCredentialMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetCredential returns metadata only; secret_ct never leaves the store here.
func (s *Store) GetCredential(ctx context.Context, id string) (*CredentialMeta, error) {
	return scanCredentialMeta(s.db.QueryRowContext(ctx, `SELECT `+credentialMetaColumns+` FROM credentials WHERE id=?`, id))
}

func scanCredentialMeta(row scanner) (*CredentialMeta, error) {
	m := &CredentialMeta{}
	var publicMeta, created, updated string
	var lastUsed sql.NullString
	err := row.Scan(&m.ID, &m.Name, &m.Kind, &m.Username, &publicMeta, &m.KeyID, &created, &updated, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.PublicMeta = parsePublicMeta(publicMeta)
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	m.LastUsedAt = parseNullTime(lastUsed)
	return m, nil
}

func parsePublicMeta(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return out
	}
	return obj
}

// CreateCredential generates the ID first so it can be bound into the AAD.
func (s *Store) CreateCredential(ctx context.Context, v *vault.Vault, in CredentialInput) (*CredentialMeta, error) {
	id := newCredentialID()
	sealed, keyID, err := v.Seal(id, in.Kind, []byte(in.Secret))
	if err != nil {
		return nil, err
	}
	publicMeta := strings.TrimSpace(in.PublicMeta)
	if publicMeta == "" {
		publicMeta = "{}"
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO credentials(id,name,kind,username,secret_ct,key_id,public_meta,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, in.Name, in.Kind, in.Username, sealed, keyID, publicMeta, ts, ts)
	if err != nil {
		return nil, translateConstraint(err)
	}
	return s.GetCredential(ctx, id)
}

// UpdateCredential re-encrypts only when a new secret is supplied, because the
// ciphertext is bound to the immutable ID and kind.
func (s *Store) UpdateCredential(ctx context.Context, v *vault.Vault, id string, p CredentialPatch) (*CredentialMeta, error) {
	kind, err := s.credentialKind(ctx, id)
	if err != nil {
		return nil, err
	}
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		sets = append(sets, "name=?")
		args = append(args, *p.Name)
	}
	if p.Username != nil {
		sets = append(sets, "username=?")
		args = append(args, *p.Username)
	}
	if p.PublicMeta != nil {
		sets = append(sets, "public_meta=?")
		args = append(args, *p.PublicMeta)
	}
	if p.Secret != nil {
		sealed, keyID, err := v.Seal(id, kind, []byte(*p.Secret))
		if err != nil {
			return nil, err
		}
		sets = append(sets, "secret_ct=?", "key_id=?")
		args = append(args, sealed, keyID)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now(), id)
	res, err := s.db.ExecContext(ctx, `UPDATE credentials SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	if err != nil {
		return nil, translateConstraint(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetCredential(ctx, id)
}

func (s *Store) credentialKind(ctx context.Context, id string) (string, error) {
	var kind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM credentials WHERE id=?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return kind, err
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id=?`, id)
	if err != nil {
		if isForeignKeyError(err) {
			return ErrInUse
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevealCredential decrypts the secret in memory. The returned plaintext is
// owned by the caller, which must not persist or log it.
func (s *Store) RevealCredential(ctx context.Context, v *vault.Vault, id string) (*CredentialSecret, error) {
	var (
		username string
		kind     string
		keyID    string
		meta     string
		sealed   []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(username,''),kind,secret_ct,key_id,public_meta
		FROM credentials WHERE id=?`, id).Scan(&username, &kind, &sealed, &keyID, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plaintext, err := v.Open(id, kind, sealed, keyID)
	if err != nil {
		return nil, err
	}
	return &CredentialSecret{ID: id, Kind: kind, Username: username, Plaintext: plaintext, PublicMeta: parsePublicMeta(meta)}, nil
}

// TouchCredentialUsed records a successful reveal without changing metadata
// timestamps.
func (s *Store) TouchCredentialUsed(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE credentials SET last_used_at=? WHERE id=?`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func isForeignKeyError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key")
}
