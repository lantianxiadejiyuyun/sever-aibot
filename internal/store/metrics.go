package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"server-aibot/internal/model"
)

const (
	MetricsMinInterval    = time.Minute
	MetricsRetentionLimit = 1440
	MetricsQueryLimit     = 1440
	MetricsBatchLimit     = 30
	maxMetricsPayload     = 64 << 10
)

// DeviceMetric associates a sample with a trusted server receipt timestamp.
type DeviceMetric struct {
	DeviceID   string        `json:"deviceId"`
	ReceivedAt time.Time     `json:"receivedAt"`
	Metrics    model.Metrics `json:"metrics"`
}

func encodeMetrics(m model.Metrics) ([]byte, error) {
	if m.CollectedAt.IsZero() {
		return nil, errors.New("metrics: collection time required")
	}
	for _, v := range []*float64{m.CPUPercent, m.Load1, m.Load5, m.Load15, m.RootDiskUsedPercent} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return nil, errors.New("metrics: invalid numeric value")
		}
	}
	for _, v := range []*float64{m.CPUPercent, m.RootDiskUsedPercent} {
		if v != nil && *v > 100 {
			return nil, errors.New("metrics: percentage exceeds 100")
		}
	}
	if m.MemUsed != nil && m.MemTotal != nil && *m.MemUsed > *m.MemTotal {
		return nil, errors.New("metrics: used memory exceeds total")
	}
	if m.RootDiskUsed != nil && m.RootDiskTotal != nil && *m.RootDiskUsed > *m.RootDiskTotal {
		return nil, errors.New("metrics: used disk exceeds total")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(body) > maxMetricsPayload {
		return nil, errors.New("metrics: payload too large")
	}
	return body, nil
}

// SaveDeviceMetrics is the legacy single-sample entry point. It inserts at most
// once per server minute interval; false,nil means throttled. For reliable
// delivery with sample IDs use SaveDeviceMetricsBatch instead.
func (s *Store) SaveDeviceMetrics(ctx context.Context, deviceID string, m model.Metrics) (bool, error) {
	return s.saveDeviceMetricsAt(ctx, deviceID, m, time.Now())
}

func (s *Store) saveDeviceMetricsAt(ctx context.Context, deviceID string, m model.Metrics, received time.Time) (bool, error) {
	if deviceID == "" {
		return false, errors.New("metrics: device required")
	}
	body, err := encodeMetrics(m)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Write first, so concurrent connections cannot race the throttle check.
	res, err := tx.ExecContext(ctx, `INSERT INTO device_metrics(device_id,received_at,sampled_at,payload)
 SELECT ?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM device_metrics WHERE device_id=? AND received_at>?)`, deviceID, received.UnixMilli(), received.UnixMilli(), string(body), deviceID, received.Add(-MetricsMinInterval).UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err = pruneMetrics(ctx, tx, deviceID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SaveDeviceMetricsBatch accepts at most 30 samples / 256 KiB. Returned IDs may
// be removed from the agent queue: they were stored, already present, coalesced
// into the same sampling minute, or expired (>24h old). Future samples >1m are
// rejected. IDs are 16 random bytes encoded as 32 hex characters. Uniqueness by
// device+ID and device+sample minute survives retries/reconnects/restarts.
// Retention and ordering use validated collection time, so backfill cannot
// evict newer samples or become the latest sample. Receipt time stays server time.
func (s *Store) SaveDeviceMetricsBatch(ctx context.Context, deviceID string, batch []model.Metrics) ([]string, error) {
	return s.saveMetricsBatchAt(ctx, deviceID, batch, time.Now())
}

func (s *Store) saveMetricsBatchAt(ctx context.Context, deviceID string, batch []model.Metrics, received time.Time) ([]string, error) {
	if deviceID == "" || len(batch) > MetricsBatchLimit {
		return nil, errors.New("metrics: invalid batch")
	}
	bodies := make([][]byte, len(batch))
	size := 0
	for i, m := range batch {
		id, err := hex.DecodeString(m.SampleID)
		if err != nil || len(id) != 16 {
			return nil, errors.New("metrics: invalid sample ID")
		}
		if m.CollectedAt.After(received.Add(time.Minute)) {
			return nil, errors.New("metrics: sample is in the future")
		}
		bodies[i], err = encodeMetrics(m)
		if err != nil {
			return nil, err
		}
		size += len(bodies[i])
	}
	if size > 256<<10 {
		return nil, errors.New("metrics: batch too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ack := make([]string, 0, len(batch))
	for i, m := range batch {
		if !m.CollectedAt.Before(received.Add(-24 * time.Hour)) {
			_, err = tx.ExecContext(ctx, `INSERT INTO device_metrics(device_id,received_at,sampled_at,sample_id,sample_minute,payload)
 VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, deviceID, received.UnixMilli(), m.CollectedAt.UnixMilli(), m.SampleID, m.CollectedAt.Unix()/60, string(bodies[i]))
			if err != nil {
				return nil, err
			}
		}
		ack = append(ack, m.SampleID)
	}
	if err = pruneMetrics(ctx, tx, deviceID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ack, nil
}

func pruneMetrics(ctx context.Context, tx *sql.Tx, deviceID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM device_metrics WHERE id IN (
 SELECT id FROM device_metrics WHERE device_id=? ORDER BY sampled_at DESC,id DESC LIMIT -1 OFFSET ?
 )`, deviceID, MetricsRetentionLimit)
	return err
}

// ListDeviceMetrics filters inclusive effective sampling-time bounds and returns
// newest samples first. Legacy samples use server receipt time as their effective
// sampling time. ReceivedAt remains separately visible. Zero bounds are unbounded;
// limit <=0 defaults to 100, and larger limits cap at 1440.
func (s *Store) ListDeviceMetrics(ctx context.Context, deviceID string, from, to time.Time, limit int) ([]DeviceMetric, error) {
	if deviceID == "" || (!from.IsZero() && !to.IsZero() && from.After(to)) {
		return nil, errors.New("metrics: invalid device or time range")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > MetricsQueryLimit {
		limit = MetricsQueryLimit
	}
	query := `SELECT received_at,payload FROM device_metrics WHERE device_id=?`
	args := []any{deviceID}
	if !from.IsZero() {
		query += ` AND sampled_at>=?`
		args = append(args, from.UnixMilli())
	}
	if !to.IsZero() {
		query += ` AND sampled_at<=?`
		args = append(args, to.UnixMilli())
	}
	query += ` ORDER BY sampled_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceMetric{}
	for rows.Next() {
		var ts int64
		var body string
		if err := rows.Scan(&ts, &body); err != nil {
			return nil, err
		}
		item := DeviceMetric{DeviceID: deviceID, ReceivedAt: time.UnixMilli(ts).UTC()}
		if err := json.Unmarshal([]byte(body), &item.Metrics); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
