package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type InspectionBatchFailure struct {
	CheckID string `json:"checkId"`
	Message string `json:"message"`
}
type InspectionBatch struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	State           string                   `json:"state"`
	CreatedAt       int64                    `json:"createdAt"`
	CompletedAt     int64                    `json:"completedAt"`
	CheckIDs        []string                 `json:"checkIds"`
	Runs            []InspectionRun          `json:"runs"`
	Failures        []InspectionBatchFailure `json:"failures"`
	Score           *int                     `json:"score"`
	Healthy         int                      `json:"healthy"`
	Failed          int                      `json:"failed"`
	Stale           int                      `json:"stale"`
	PreviousBatchID string                   `json:"previousBatchId,omitempty"`
	PreviousScore   *int                     `json:"previousScore"`
	ScoreDelta      *int                     `json:"scoreDelta"`
}

func (s *Store) CreateInspectionBatch(ctx context.Context, name string, ids []string) (InspectionBatch, error) {
	b := InspectionBatch{ID: newID("batch"), Name: name, State: "running", CreatedAt: time.Now().Unix(), CheckIDs: ids, Runs: []InspectionRun{}, Failures: []InspectionBatchFailure{}}
	body, err := json.Marshal(b)
	if err != nil {
		return b, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO inspection_batches(id,name,state,created_at,report) VALUES(?,?,?,?,?)`, b.ID, b.Name, b.State, b.CreatedAt, string(body))
	return b, err
}
func (s *Store) FinishInspectionBatch(ctx context.Context, b InspectionBatch) error {
	b.CompletedAt = time.Now().Unix()
	b.Healthy, b.Failed, b.Stale = 0, 0, len(b.Failures)
	for _, run := range b.Runs {
		switch run.State {
		case "up":
			b.Healthy++
		case "down":
			b.Failed++
		default:
			b.Stale++
		}
	}
	if evaluated := b.Healthy + b.Failed; evaluated > 0 {
		score := 100 * b.Healthy / evaluated
		b.Score = &score
	}
	// Compare only identical check sets. A changed batch composition is not a
	// meaningful score trend and must not silently compare unlike reports.
	rows, err := s.db.QueryContext(ctx, `SELECT report FROM inspection_batches WHERE state='complete' AND id<>? ORDER BY created_at DESC,rowid DESC LIMIT 100`, b.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var previous InspectionBatch
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			rows.Close()
			return err
		}
		if sameCheckSet(b.CheckIDs, previous.CheckIDs) {
			b.PreviousBatchID = previous.ID
			b.PreviousScore = previous.Score
			if b.Score != nil && previous.Score != nil {
				delta := *b.Score - *previous.Score
				b.ScoreDelta = &delta
			}
			break
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return operationAffected(s.db.ExecContext(ctx, `UPDATE inspection_batches SET state=?,completed_at=?,report=? WHERE id=?`, b.State, b.CompletedAt, string(raw), b.ID))
}
func sameCheckSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			return false
		}
	}
	return true
}
func (s *Store) GetInspectionBatch(ctx context.Context, id string) (InspectionBatch, error) {
	var b InspectionBatch
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT report FROM inspection_batches WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	err = json.Unmarshal([]byte(raw), &b)
	return b, err
}
func (s *Store) ListInspectionBatches(ctx context.Context) ([]InspectionBatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT report FROM inspection_batches ORDER BY created_at DESC,rowid DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InspectionBatch{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var b InspectionBatch
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// An interrupted process cannot resume a partially executed batch safely. Mark
// expired batches explicitly; never manufacture passing results after restart.
func (s *Store) RecoverInspectionBatches(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT report FROM inspection_batches WHERE state='running' AND created_at<?`, time.Now().Add(-16*time.Minute).Unix())
	if err != nil {
		return err
	}
	batches := []InspectionBatch{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var b InspectionBatch
		if err = json.Unmarshal([]byte(raw), &b); err != nil {
			rows.Close()
			return err
		}
		batches = append(batches, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range batches {
		b.State = "interrupted"
		seen := map[string]bool{}
		for _, run := range b.Runs {
			seen[run.CheckID] = true
		}
		for _, failure := range b.Failures {
			seen[failure.CheckID] = true
		}
		for _, id := range b.CheckIDs {
			if !seen[id] {
				b.Failures = append(b.Failures, InspectionBatchFailure{CheckID: id, Message: "批次执行进程中断或超时"})
			}
		}
		if err = s.FinishInspectionBatch(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
