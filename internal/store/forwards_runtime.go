package store

import (
	"context"
	"errors"
	"math"
	"server-aibot/internal/model"
	"time"
)

// CommandForward advances the desired revision. A stopped forward remains in
// stopping until the matching node acknowledgement confirms resource closure.
var ErrForwardConflict = errors.New("forward changed or requires confirmed stop")

func (s *Store) CommandForward(ctx context.Context, id string, start bool) (*model.Forward, error) {
	return s.CommandForwardChecked(ctx, id, start, -1)
}
func (s *Store) CommandForwardChecked(ctx context.Context, id string, start bool, revision int64) (*model.Forward, error) {
	state := "stopping"
	if start {
		state = "applying"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE forwards SET state=CASE WHEN ?='stopping' AND state='saved' THEN 'stopped' ELSE ? END,revision=revision+1,last_error='',updated_at=? WHERE id=? AND (?<0 OR revision=?)`, state, state, now(), id, revision, revision)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrForwardConflict
	}
	return s.GetForward(ctx, id)
}

// AckForward uses an atomic compare-and-set; delayed or cross-node results can
// never resurrect a stopped rule or overwrite a newer revision.
func (s *Store) AckForward(ctx context.Context, nodeID, id string, revision int64, state, message string) error {
	allowed := "('applying','applied','observed')"
	if state == "stopped" || state == "stopping" {
		allowed = "('stopping')"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE forwards SET state=CASE WHEN state='observed' AND ?='applied' THEN state ELSE ? END,last_error=?,updated_at=? WHERE id=? AND node_id=? AND revision=? AND state IN `+allowed, state, state, message, now(), id, nodeID, revision)
	return err
}

func (s *Store) NextNodeConfigRevision(ctx context.Context, id string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO node_config_revisions(node_id,revision) VALUES(?,1) ON CONFLICT(node_id) DO UPDATE SET revision=revision+1 RETURNING revision`, id).Scan(&revision)
	return revision, err
}

// RecordForwardTraffic accepts unique, directional reports only for their owning
// node. The UTC day comes from the collector interval, capped to seven days old.
func (s *Store) RecordForwardTraffic(ctx context.Context, nodeID string, f model.AgentFrame) error {
	if f.UploadBytes < 0 || f.DownloadBytes < 0 || f.UploadBytes > math.MaxInt64-f.DownloadBytes || len(f.Ref) == 0 || len(f.Ref) > 128 {
		return errors.New("invalid traffic report")
	}
	if f.UploadBytes+f.DownloadBytes == 0 {
		return nil
	}
	at := time.Unix(f.TS, 0).UTC()
	current := time.Now().UTC()
	if at.After(current.Add(time.Minute)) || at.Before(current.AddDate(0, 0, -7)) {
		return errors.New("invalid traffic timestamp")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT node_id FROM forwards WHERE id=?`, f.ForwardID).Scan(&owner); err != nil {
		return err
	}
	if owner != nodeID {
		return errors.New("traffic node mismatch")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO forward_traffic_reports(node_id,report_id,received_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, nodeID, f.Ref, now())
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO forward_traffic_daily(forward_id,day,upload_bytes,download_bytes) VALUES(?,?,?,?) ON CONFLICT(forward_id,day) DO UPDATE SET upload_bytes=upload_bytes+excluded.upload_bytes,download_bytes=download_bytes+excluded.download_bytes`, f.ForwardID, at.Format("2006-01-02"), f.UploadBytes, f.DownloadBytes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE forwards SET traffic_bytes=traffic_bytes+?,state=CASE WHEN revision=? AND state IN ('applying','applied','observed') THEN 'observed' ELSE state END WHERE id=? AND node_id=?`, f.UploadBytes+f.DownloadBytes, f.Revision, f.ForwardID, nodeID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ForwardTrafficDays(ctx context.Context, id string, days int) ([]model.ForwardTrafficDay, error) {
	if days < 1 || days > 366 {
		days = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT day,upload_bytes,download_bytes FROM forward_traffic_daily WHERE forward_id=? AND day>=? ORDER BY day`, id, time.Now().UTC().AddDate(0, 0, 1-days).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ForwardTrafficDay{}
	for rows.Next() {
		var d model.ForwardTrafficDay
		if err := rows.Scan(&d.Day, &d.UploadBytes, &d.DownloadBytes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AllTrafficDays(ctx context.Context, days int) ([]model.ForwardTrafficDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day,SUM(upload_bytes),SUM(download_bytes) FROM forward_traffic_daily WHERE day>=? GROUP BY day ORDER BY day`, time.Now().UTC().AddDate(0, 0, 1-days).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ForwardTrafficDay{}
	for rows.Next() {
		var d model.ForwardTrafficDay
		if err := rows.Scan(&d.Day, &d.UploadBytes, &d.DownloadBytes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateForwardRuntime prevents an edit racing with stop/start from restoring
// an obsolete state, and preserves the desired-state lifecycle for revisions.
func (s *Store) UpdateForwardRuntime(ctx context.Context, f model.Forward) (*model.Forward, error) {
	var device, node any
	if f.DeviceID != "" {
		device = f.DeviceID
	}
	if f.NodeID != "" {
		node = f.NodeID
	}
	result, err := s.db.ExecContext(ctx, `UPDATE forwards SET name=?,kind=?,device_id=?,node_id=?,client_id=?,target_ip=?,target_port=?,remote_port=?,domain=?,state=?,last_error=?,tags=?,options=?,updated_at=?,revision=revision+1 WHERE id=? AND revision=? AND state<>'stopping'`, f.Name, f.Kind, device, node, f.ClientID, f.TargetIP, f.TargetPort, f.RemotePort, f.Domain, f.State, f.LastError, f.Tags, f.Options, now(), f.ID, f.Revision)
	if err != nil {
		return nil, translateConstraint(err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrForwardConflict
	}
	return s.GetForward(ctx, f.ID)
}
func (s *Store) DeleteInactiveForward(ctx context.Context, id string, revision int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM forwards WHERE id=? AND revision=? AND state IN ('saved','stopped')`, id, revision)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrForwardConflict
	}
	return nil
}

func (s *Store) RevokeDeviceTunnels(ctx context.Context, id string) (int64, error) {
	stamp := time.Now().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_tunnel_revocations(device_id,revoked_before) VALUES(?,?) ON CONFLICT(device_id) DO UPDATE SET revoked_before=excluded.revoked_before`, id, stamp); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE forwards SET revision=revision+1,state='applying',updated_at=? WHERE (device_id=? OR client_id=?) AND state IN ('applying','applied','observed')`, now(), id, id); err != nil {
		return 0, err
	}
	return stamp, tx.Commit()
}
func (s *Store) DeviceTunnelRevocations(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id,revoked_before FROM device_tunnel_revocations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var stamp int64
		if err := rows.Scan(&id, &stamp); err != nil {
			return nil, err
		}
		out[id] = stamp
	}
	return out, rows.Err()
}

func (s *Store) RecordForwardPath(ctx context.Context, node, id string, revision int64, path string) error {
	if path != "direct" && path != "relay" && path != "unavailable" {
		return errors.New("invalid path")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO forward_paths(forward_id,revision,path,updated_at) SELECT id,revision,?,? FROM forwards WHERE id=? AND node_id=? AND revision=? AND state IN ('applying','applied','observed') ON CONFLICT(forward_id) DO UPDATE SET revision=excluded.revision,path=excluded.path,updated_at=excluded.updated_at`, path, now(), id, node, revision)
	return err
}
func (s *Store) ForwardPath(ctx context.Context, id string, revision int64) string {
	var path string
	if err := s.db.QueryRowContext(ctx, `SELECT path FROM forward_paths WHERE forward_id=? AND revision=?`, id, revision).Scan(&path); err != nil {
		return "not_observed"
	}
	return path
}
