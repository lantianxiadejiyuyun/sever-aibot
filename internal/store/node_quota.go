package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"server-aibot/internal/model"
)

// QuotaStatus counts durable reservations, not successfully delivered traffic.
type QuotaStatus struct {
	LimitBytes     int64  `json:"limitBytes"`
	ReservedBytes  int64  `json:"reservedBytes"`
	RemainingBytes int64  `json:"remainingBytes"`
	PeriodStart    string `json:"periodStart"`
	ResetAt        int64  `json:"resetAt"`
}

func ValidateNodeQuota(n model.Node) error {
	if math.IsNaN(n.LimitTotalGB) || math.IsInf(n.LimitTotalGB, 0) || n.LimitTotalGB < 0 || n.LimitTotalGB >= float64(math.MaxInt64)/1e9 {
		return errors.New("limitTotalGb must be finite, nonnegative and less than 9223372036 GB")
	}
	if n.LimitTotalGB > 0 && n.LimitTotalGB*1e9 < 1 {
		return errors.New("limitTotalGb must allow at least one byte")
	}
	if n.LimitKind != "" && n.LimitKind != "total" && n.LimitKind != "monthly" {
		return errors.New("limitKind must be total or monthly")
	}
	if n.LimitResetDay < 0 || n.LimitResetDay > 31 {
		return errors.New("limitResetDay must be between 0 and 31")
	}
	return nil
}

func quotaPeriod(n model.Node, at time.Time) (string, int64) {
	if n.LimitKind != "monthly" {
		return "", 0
	}
	at = at.UTC()
	day := n.LimitResetDay
	if day == 0 {
		day = 1
	}
	boundary := func(year int, month time.Month) time.Time {
		last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
		d := day
		if d > last {
			d = last
		}
		return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
	}
	start := boundary(at.Year(), at.Month())
	if at.Before(start) {
		previous := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		start = boundary(previous.Year(), previous.Month())
	}
	next := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return start.Format("2006-01-02"), boundary(next.Year(), next.Month()).Unix()
}

type quotaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readQuota(ctx context.Context, q quotaQuerier, nodeID string, at time.Time) (QuotaStatus, error) {
	var n model.Node
	if err := q.QueryRowContext(ctx, `SELECT limit_total_gb,limit_kind,limit_reset_day FROM nodes WHERE id=?`, nodeID).Scan(&n.LimitTotalGB, &n.LimitKind, &n.LimitResetDay); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QuotaStatus{}, ErrNotFound
		}
		return QuotaStatus{}, err
	}
	if err := ValidateNodeQuota(n); err != nil {
		return QuotaStatus{}, err
	}
	start, end := quotaPeriod(n, at)
	status := QuotaStatus{LimitBytes: int64(n.LimitTotalGB * 1e9), PeriodStart: start, ResetAt: end}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes),0) FROM node_quota_daily WHERE node_id=? AND day>=?`, nodeID, start).Scan(&status.ReservedBytes); err != nil {
		return status, err
	}
	status.RemainingBytes = max(0, status.LimitBytes-status.ReservedBytes)
	return status, nil
}

func (s *Store) NodeQuota(ctx context.Context, nodeID string) (QuotaStatus, error) {
	return readQuota(ctx, s.db, nodeID, time.Now())
}

// ReserveNodeQuota serializes policy reads, deduplication and charging under a
// SQLite write lock. A committed grant is never refunded, including lost replies
// and process crashes. Requests are capped at one copy buffer (65535 bytes).
func (s *Store) ReserveNodeQuota(ctx context.Context, nodeID, ref string, requested int64) (int64, int64, error) {
	return s.reserveNodeQuotaAt(ctx, nodeID, ref, requested, time.Now().UTC())
}
func (s *Store) reserveNodeQuotaAt(ctx context.Context, nodeID, ref string, requested int64, at time.Time) (int64, int64, error) {
	if len(ref) != 32 || requested < 1 || requested > 65535 {
		return 0, 0, errors.New("invalid quota reservation")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, 0, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var oldRequest, grant, expiry int64
	err = conn.QueryRowContext(ctx, `SELECT requested_bytes,granted_bytes,expires_at FROM node_quota_reservations WHERE node_id=? AND request_id=?`, nodeID, ref).Scan(&oldRequest, &grant, &expiry)
	if err == nil {
		if oldRequest != requested {
			return 0, 0, errors.New("quota reservation payload mismatch")
		}
		return grant, expiry, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	status, err := readQuota(ctx, conn, nodeID, at)
	if err != nil {
		return 0, 0, err
	}
	grant = requested
	if status.LimitBytes > 0 {
		grant = min(requested, status.RemainingBytes)
	}
	// Limit grants to a short lifetime, also bounded by the quota period. The
	// reservation is attributed to its grant time even if a write completes later.
	expiry = at.Add(10 * time.Second).Unix()
	if status.ResetAt > 0 && status.ResetAt < expiry {
		expiry = status.ResetAt
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO node_quota_reservations(node_id,request_id,requested_bytes,granted_bytes,expires_at) VALUES(?,?,?,?,?)`, nodeID, ref, requested, grant, expiry); err != nil {
		return 0, 0, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO node_quota_daily(node_id,day,reserved_bytes) VALUES(?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET reserved_bytes=reserved_bytes+excluded.reserved_bytes`, nodeID, at.Format("2006-01-02"), grant); err != nil {
		return 0, 0, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, 0, err
	}
	return grant, expiry, nil
}
