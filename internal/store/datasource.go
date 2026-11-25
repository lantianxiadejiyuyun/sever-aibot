package store

import (
	"context"
	"database/sql"
)

type DataSource struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AssetID      string `json:"assetId"`
	CredentialID string `json:"credentialId"`
	Database     string `json:"database"`
	TLSMode      string `json:"tlsMode"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

const datasourceColumns = `id,name,asset_id,credential_id,database_name,tls_mode,enabled,created_at,updated_at`

func scanDataSource(row scanner) (*DataSource, error) {
	var d DataSource
	var enabled int
	err := row.Scan(&d.ID, &d.Name, &d.AssetID, &d.CredentialID, &d.Database, &d.TLSMode, &enabled, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	d.Enabled = enabled != 0
	return &d, err
}
func (s *Store) GetDataSource(ctx context.Context, id string) (*DataSource, error) {
	return scanDataSource(s.db.QueryRowContext(ctx, `SELECT `+datasourceColumns+` FROM datasources WHERE id=?`, id))
}
func (s *Store) ListDataSources(ctx context.Context) ([]DataSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+datasourceColumns+` FROM datasources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DataSource{}
	for rows.Next() {
		d, err := scanDataSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}
func (s *Store) CreateDataSource(ctx context.Context, d DataSource) (*DataSource, error) {
	d.ID = newID("ds")
	_, err := s.db.ExecContext(ctx, `INSERT INTO datasources(`+datasourceColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, d.Name, d.AssetID, d.CredentialID, d.Database, d.TLSMode, b2i(d.Enabled), now(), now())
	if err != nil {
		return nil, translateConstraint(err)
	}
	return s.GetDataSource(ctx, d.ID)
}
func (s *Store) UpdateDataSource(ctx context.Context, d DataSource) (*DataSource, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE datasources SET name=?,asset_id=?,credential_id=?,database_name=?,tls_mode=?,enabled=?,updated_at=? WHERE id=?`, d.Name, d.AssetID, d.CredentialID, d.Database, d.TLSMode, b2i(d.Enabled), now(), d.ID)
	if err != nil {
		return nil, translateConstraint(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	return s.GetDataSource(ctx, d.ID)
}
func (s *Store) DeleteDataSource(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM datasources WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}
