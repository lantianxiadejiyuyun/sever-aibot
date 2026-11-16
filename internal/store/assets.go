package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Asset is an inventory entry: "who do I connect to, and through whom".
type Asset struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	Port         int       `json:"port"`
	Protocol     string    `json:"protocol"`
	ViaDeviceID  string    `json:"viaDeviceId"`
	ViaAssetID   string    `json:"viaAssetId"`
	CredentialID string    `json:"credentialId"`
	HostKey      string    `json:"hostKey"`
	Tags         string    `json:"tags"`
	Enabled      bool      `json:"enabled"`
	Notes        string    `json:"notes"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// AssetCheck is one TCP/protocol probe result reported by an agent.
type AssetCheck struct {
	ID        int64     `json:"id"`
	AssetID   string    `json:"assetId"`
	DeviceID  string    `json:"deviceId"`
	OK        bool      `json:"ok"`
	LatencyMS *int      `json:"latencyMs,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

const assetColumns = `id,name,address,port,protocol,COALESCE(via_device_id,''),COALESCE(via_asset_id,''),
	COALESCE(credential_id,''),host_key,tags,enabled,notes,created_at,updated_at`

// ListAssets filters by jump device, tag token and a free-text query.
func (s *Store) ListAssets(ctx context.Context, device, tag, q string) ([]Asset, error) {
	query := `SELECT ` + assetColumns + ` FROM assets WHERE 1=1`
	args := []any{}
	if device != "" {
		query += ` AND via_device_id=?`
		args = append(args, device)
	}
	if tag != "" {
		query += ` AND (',' || tags || ',') LIKE ?`
		args = append(args, "%,"+tag+",%")
	}
	if q != "" {
		query += ` AND (name LIKE ? OR address LIKE ? OR notes LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	query += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) GetAsset(ctx context.Context, id string) (*Asset, error) {
	return scanAsset(s.db.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE id=?`, id))
}

func scanAsset(row scanner) (*Asset, error) {
	a := &Asset{}
	var enabled int
	var created, updated string
	err := row.Scan(&a.ID, &a.Name, &a.Address, &a.Port, &a.Protocol, &a.ViaDeviceID, &a.ViaAssetID,
		&a.CredentialID, &a.HostKey, &a.Tags, &enabled, &a.Notes, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled != 0
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

func (s *Store) CreateAsset(ctx context.Context, a Asset) (*Asset, error) {
	a.ID = newID("ast")
	if a.Address == "" {
		return nil, errors.New("asset address is empty")
	}
	if a.Port == 0 {
		a.Port = 22
	}
	if a.Protocol == "" {
		a.Protocol = "ssh"
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO assets(id,name,address,port,protocol,via_device_id,via_asset_id,
		credential_id,host_key,tags,enabled,notes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, a.Address, a.Port, a.Protocol, nullable(a.ViaDeviceID), nullable(a.ViaAssetID),
		nullable(a.CredentialID), a.HostKey, a.Tags, b2i(a.Enabled), a.Notes, ts, ts)
	if err != nil {
		return nil, translateConstraint(err)
	}
	return s.GetAsset(ctx, a.ID)
}

func (s *Store) UpdateAsset(ctx context.Context, a Asset) (*Asset, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE assets SET name=?,address=?,port=?,protocol=?,via_device_id=?,
		via_asset_id=?,credential_id=?,host_key=?,tags=?,enabled=?,notes=?,updated_at=? WHERE id=?`,
		a.Name, a.Address, a.Port, a.Protocol, nullable(a.ViaDeviceID), nullable(a.ViaAssetID),
		nullable(a.CredentialID), a.HostKey, a.Tags, b2i(a.Enabled), a.Notes, now(), a.ID)
	if err != nil {
		return nil, translateConstraint(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetAsset(ctx, a.ID)
}

func (s *Store) DeleteAsset(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM assets WHERE id=?`, id)
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

// ListAssetChecks returns the most recent probes for one asset.
func (s *Store) ListAssetChecks(ctx context.Context, assetID string, limit int) ([]AssetCheck, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id,asset_id,device_id,ok,latency_ms,error,checked_at FROM asset_checks`
	args := []any{}
	if assetID != "" {
		query += ` WHERE asset_id=?`
		args = append(args, assetID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetCheck{}
	for rows.Next() {
		var c AssetCheck
		var ok int
		var latency sql.NullInt64
		var checkErr sql.NullString
		var checked string
		if err := rows.Scan(&c.ID, &c.AssetID, &c.DeviceID, &ok, &latency, &checkErr, &checked); err != nil {
			return nil, err
		}
		c.OK = ok != 0
		if latency.Valid {
			n := int(latency.Int64)
			c.LatencyMS = &n
		}
		c.Error = checkErr.String
		c.CheckedAt = parseTime(checked)
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullable(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
