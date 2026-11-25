package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"server-aibot/internal/vault"
)

type OperationCheck struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	TargetKind      string  `json:"targetKind"`
	TargetID        string  `json:"targetId"`
	Path            string  `json:"path"`
	Scheme          string  `json:"scheme"`
	TimeoutMS       int     `json:"timeoutMs"`
	IntervalSeconds int     `json:"intervalSeconds"`
	Enabled         bool    `json:"enabled"`
	State           string  `json:"state"`
	CheckedAt       int64   `json:"checkedAt"`
	Metric          string  `json:"metric"`
	Threshold       float64 `json:"threshold"`
	MaxAgeSeconds   int     `json:"maxAgeSeconds"`
	Profile         string  `json:"profile"`
}
type InspectionRun struct {
	ID            string           `json:"id"`
	CheckID       string           `json:"checkId"`
	CheckName     string           `json:"checkName"`
	State         string           `json:"state"`
	Message       string           `json:"message"`
	LatencyMS     int64            `json:"latencyMs"`
	CreatedAt     int64            `json:"createdAt"`
	PreviousState string           `json:"previousState"`
	Score         *int             `json:"score"`
	Detail        InspectionDetail `json:"detail"`
}
type InspectionEvidence struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Output  string `json:"output,omitempty"`
}
type InspectionDetail struct {
	Source   string               `json:"source"`
	Profile  string               `json:"profile,omitempty"`
	Evidence []InspectionEvidence `json:"evidence,omitempty"`
}

// Inspection scores are explicit: healthy=100, failed=0, unverified has no score.
func inspectionScore(state string) *int {
	if state == "stale" {
		return nil
	}
	score := 0
	if state == "up" {
		score = 100
	}
	return &score
}

type OperationAlert struct {
	ID           string `json:"id"`
	CheckID      string `json:"checkId"`
	State        string `json:"state"`
	Message      string `json:"message"`
	Acknowledged bool   `json:"acknowledged"`
	SilenceUntil int64  `json:"silenceUntil"`
	Resolved     bool   `json:"resolved"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}
type NotificationChannel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}
type OperationNotification struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Read      bool   `json:"read"`
	CreatedAt int64  `json:"createdAt"`
}
type NotificationDelivery struct {
	ID          string `json:"id"`
	EventID     string `json:"eventId"`
	ChannelID   string `json:"channelId"`
	Payload     string `json:"-"`
	State       string `json:"state"`
	Attempts    int    `json:"attempts"`
	NextAttempt int64  `json:"nextAttempt"`
	LastError   string `json:"lastError"`
	CreatedAt   int64  `json:"createdAt"`
}
type NotificationLog struct {
	ID        int64  `json:"id"`
	OutboxID  string `json:"outboxId"`
	Attempt   int    `json:"attempt"`
	State     string `json:"state"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Store) ListOperationChecks(ctx context.Context) ([]OperationCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT definition,state,checked_at FROM operation_checks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperationCheck{}
	for rows.Next() {
		var raw, state string
		var checked int64
		if err = rows.Scan(&raw, &state, &checked); err != nil {
			return nil, err
		}
		var c OperationCheck
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		c.State = state
		c.CheckedAt = checked
		if checked == 0 || (c.Enabled && time.Now().Unix()-checked > int64(c.IntervalSeconds*2+30)) {
			c.State = "stale"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) GetOperationCheck(ctx context.Context, id string) (OperationCheck, error) {
	var c OperationCheck
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT definition,state,checked_at FROM operation_checks WHERE id=?`, id).Scan(&raw, &c.State, &c.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	state, checked := c.State, c.CheckedAt
	err = json.Unmarshal([]byte(raw), &c)
	c.State = state
	c.CheckedAt = checked
	return c, err
}
func (s *Store) SaveOperationCheck(ctx context.Context, c OperationCheck, create bool) (OperationCheck, error) {
	if create {
		c.ID = newID("chk")
	}
	c.State = ""
	c.CheckedAt = 0
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO operation_checks(id,definition,enabled,interval_seconds) VALUES(?,?,?,?)`, c.ID, string(raw), c.Enabled, c.IntervalSeconds)
	} else {
		var res sql.Result
		res, err = s.db.ExecContext(ctx, `UPDATE operation_checks SET definition=?,enabled=?,interval_seconds=?,next_run=0 WHERE id=? AND lease_until<=?`, string(raw), c.Enabled, c.IntervalSeconds, c.ID, time.Now().Unix())
		if err == nil {
			n, _ := res.RowsAffected()
			if n == 0 {
				return c, ErrInUse
			}
		}
	}
	if err != nil {
		return c, err
	}
	return s.GetOperationCheck(ctx, c.ID)
}
func (s *Store) DeleteOperationCheck(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM operation_checks WHERE id=? AND lease_until<=?`, id, time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInUse
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operation_alerts SET resolved=1,updated_at=? WHERE check_id=?`, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimOperationCheck serializes manual and scheduled execution across workers.
func (s *Store) ClaimOperationCheck(ctx context.Context, id string, manual bool) (bool, error) {
	now := time.Now().Unix()
	q := `UPDATE operation_checks SET lease_until=? WHERE id=? AND lease_until<=?`
	if !manual {
		q += ` AND enabled=1 AND next_run<=?`
	}
	args := []any{now + 60, id, now}
	if !manual {
		args = append(args, now)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishOperationCheck commits report, state transition, inbox and outbox atomically.
func (s *Store) FinishOperationCheck(ctx context.Context, c OperationCheck, state, message string, latency int64) (InspectionRun, error) {
	return s.FinishOperationCheckDetail(ctx, c, state, message, latency, InspectionDetail{})
}
func (s *Store) FinishOperationCheckDetail(ctx context.Context, c OperationCheck, state, message string, latency int64, detail InspectionDetail) (InspectionRun, error) {
	now := time.Now().Unix()
	run := InspectionRun{ID: newID("run"), CheckID: c.ID, CheckName: c.Name, State: state, Message: message, LatencyMS: latency, CreatedAt: now, Score: inspectionScore(state), Detail: detail}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer tx.Rollback()
	// Acquire SQLite's write lock before reads, avoiding a deferred-transaction
	// snapshot upgrade failure when other probes finish concurrently.
	if _, err = tx.ExecContext(ctx, `UPDATE operation_checks SET lease_until=lease_until WHERE id=?`, c.ID); err != nil {
		return run, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT state FROM operation_checks WHERE id=?`, c.ID).Scan(&previous)
	if err != nil {
		return run, err
	}
	err = tx.QueryRowContext(ctx, `SELECT state FROM inspection_runs WHERE check_id=? ORDER BY rowid DESC LIMIT 1`, c.ID).Scan(&run.PreviousState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return run, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO inspection_runs(id,check_id,check_name,state,message,latency_ms,created_at,detail) VALUES(?,?,?,?,?,?,?,?)`, run.ID, c.ID, c.Name, state, message, latency, now, string(detailJSON))
	if err != nil {
		return run, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE operation_checks SET state=?,checked_at=?,next_run=?,lease_until=0 WHERE id=?`, state, now, now+int64(c.IntervalSeconds), c.ID)
	if err != nil {
		return run, err
	}
	var alertID string
	var silence int64
	err = tx.QueryRowContext(ctx, `SELECT id,silence_until FROM operation_alerts WHERE check_id=? AND resolved=0`, c.ID).Scan(&alertID, &silence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	transition := state != previous || (state != "up" && alertID == "")
	if state == "up" {
		_, err = tx.ExecContext(ctx, `UPDATE operation_alerts SET resolved=1,updated_at=? WHERE check_id=? AND resolved=0`, now, c.ID)
	} else if alertID == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO operation_alerts(id,check_id,state,message,created_at,updated_at) VALUES(?,?,?,?,?,?)`, newID("alt"), c.ID, state, message, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE operation_alerts SET state=?,message=?,updated_at=? WHERE id=?`, state, message, now, alertID)
	}
	if err != nil {
		return run, err
	}
	if transition && silence <= now {
		text := c.Name + ": " + state + " — " + message
		_, err = tx.ExecContext(ctx, `INSERT INTO operation_notifications(id,message,created_at) VALUES(?,?,?)`, run.ID, text, now)
		if err != nil {
			return run, err
		}
		payload, _ := json.Marshal(map[string]any{"eventId": run.ID, "checkId": c.ID, "name": c.Name, "state": state, "message": message, "text": text, "createdAt": now})
		_, err = tx.ExecContext(ctx, `INSERT INTO notification_outbox(id,event_id,channel_id,payload,created_at) SELECT ?||':'||id,?,id,?,? FROM notification_channels WHERE enabled=1 ON CONFLICT(event_id,channel_id) DO NOTHING`, run.ID, run.ID, string(payload), now)
		if err != nil {
			return run, err
		}
	}
	return run, tx.Commit()
}
func (s *Store) ListInspectionRuns(ctx context.Context) ([]InspectionRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.check_id,r.check_name,r.state,r.message,r.latency_ms,r.created_at,r.detail,COALESCE((SELECT p.state FROM inspection_runs p WHERE p.check_id=r.check_id AND p.rowid<r.rowid ORDER BY p.rowid DESC LIMIT 1),'') FROM inspection_runs r ORDER BY r.created_at DESC,r.rowid DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InspectionRun{}
	for rows.Next() {
		var v InspectionRun
		var detail string
		if err = rows.Scan(&v.ID, &v.CheckID, &v.CheckName, &v.State, &v.Message, &v.LatencyMS, &v.CreatedAt, &detail, &v.PreviousState); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(detail), &v.Detail); err != nil {
			return nil, err
		}
		v.Score = inspectionScore(v.State)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ListOperationAlerts(ctx context.Context) ([]OperationAlert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,check_id,state,message,acknowledged,silence_until,resolved,created_at,updated_at FROM operation_alerts ORDER BY resolved,created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperationAlert{}
	for rows.Next() {
		var v OperationAlert
		if err = rows.Scan(&v.ID, &v.CheckID, &v.State, &v.Message, &v.Acknowledged, &v.SilenceUntil, &v.Resolved, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func operationAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) AcknowledgeOperationAlert(ctx context.Context, id string) error {
	return operationAffected(s.db.ExecContext(ctx, `UPDATE operation_alerts SET acknowledged=1,updated_at=? WHERE id=?`, time.Now().Unix(), id))
}
func (s *Store) SilenceOperationAlert(ctx context.Context, id string, until int64) error {
	return operationAffected(s.db.ExecContext(ctx, `UPDATE operation_alerts SET silence_until=?,updated_at=? WHERE id=?`, until, time.Now().Unix(), id))
}
func (s *Store) ListOperationNotifications(ctx context.Context) ([]OperationNotification, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,message,is_read,created_at FROM operation_notifications ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OperationNotification{}
	for rows.Next() {
		var v OperationNotification
		if err = rows.Scan(&v.ID, &v.Message, &v.Read, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ReadOperationNotification(ctx context.Context, id string) error {
	return operationAffected(s.db.ExecContext(ctx, `UPDATE operation_notifications SET is_read=1 WHERE id=?`, id))
}
func (s *Store) ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,enabled FROM notification_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationChannel{}
	for rows.Next() {
		var v NotificationChannel
		if err = rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Enabled); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// The whole webhook URL is secret: bot tokens often live in its path.
func (s *Store) SaveNotificationChannel(ctx context.Context, v *vault.Vault, c NotificationChannel, webhook string, create bool) (NotificationChannel, error) {
	if create {
		c.ID = newID("chn")
	}
	var sealed []byte
	var key string
	var err error
	if webhook != "" {
		sealed, key, err = v.Seal(c.ID, "webhook", []byte(webhook))
		if err != nil {
			return c, err
		}
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO notification_channels VALUES(?,?,?,?,?,?)`, c.ID, c.Name, c.Kind, c.Enabled, sealed, key)
	} else if webhook != "" {
		err = operationAffected(s.db.ExecContext(ctx, `UPDATE notification_channels SET name=?,kind=?,enabled=?,secret_ct=?,key_id=? WHERE id=?`, c.Name, c.Kind, c.Enabled, sealed, key, c.ID))
	} else {
		err = operationAffected(s.db.ExecContext(ctx, `UPDATE notification_channels SET name=?,kind=?,enabled=? WHERE id=?`, c.Name, c.Kind, c.Enabled, c.ID))
	}
	return c, err
}
func (s *Store) NotificationChannelSecret(ctx context.Context, v *vault.Vault, id string) (NotificationChannel, string, error) {
	var c NotificationChannel
	var sealed []byte
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,enabled,secret_ct,key_id FROM notification_channels WHERE id=?`, id).Scan(&c.ID, &c.Name, &c.Kind, &c.Enabled, &sealed, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return c, "", ErrNotFound
	}
	if err != nil {
		return c, "", err
	}
	plain, err := v.Open(id, "webhook", sealed, key)
	return c, string(plain), err
}
func (s *Store) DeleteNotificationChannel(ctx context.Context, id string) error {
	return operationAffected(s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=?`, id))
}
func (s *Store) ListNotificationOutbox(ctx context.Context) ([]NotificationDelivery, error) {
	return s.notificationOutbox(ctx, false)
}
func (s *Store) DueNotificationOutbox(ctx context.Context) ([]NotificationDelivery, error) {
	return s.notificationOutbox(ctx, true)
}
func (s *Store) notificationOutbox(ctx context.Context, due bool) ([]NotificationDelivery, error) {
	q := `SELECT id,event_id,channel_id,payload,state,attempts,next_attempt,last_error,created_at FROM notification_outbox`
	args := []any{}
	if due {
		q += ` WHERE state IN ('pending','sending') AND next_attempt<=? AND lease_until<=?`
		args = append(args, time.Now().Unix(), time.Now().Unix())
	}
	q += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDelivery{}
	for rows.Next() {
		var v NotificationDelivery
		if err = rows.Scan(&v.ID, &v.EventID, &v.ChannelID, &v.Payload, &v.State, &v.Attempts, &v.NextAttempt, &v.LastError, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ClaimNotificationDelivery(ctx context.Context, id string) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE notification_outbox SET state='sending',lease_until=?,attempts=attempts+1 WHERE id=? AND state IN ('pending','sending') AND next_attempt<=? AND lease_until<=?`, now+60, id, now, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (s *Store) FinishNotificationDelivery(ctx context.Context, d NotificationDelivery, message string) error {
	state := "sent"
	next := int64(0)
	attempt := d.Attempts + 1
	if message != "" {
		state = "pending"
		if attempt >= 5 {
			state = "failed"
		}
		next = time.Now().Unix() + int64(15*(1<<min(attempt, 8)))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET state=?,next_attempt=?,lease_until=0,last_error=? WHERE id=?`, state, next, message, d.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_logs(outbox_id,attempt,state,message,created_at) VALUES(?,?,?,?,?)`, d.ID, attempt, state, message, time.Now().Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RetryNotificationDelivery(ctx context.Context, id string) error {
	return operationAffected(s.db.ExecContext(ctx, `UPDATE notification_outbox SET state='pending',attempts=0,next_attempt=0,last_error='' WHERE id=? AND state='failed' AND lease_until<=?`, id, time.Now().Unix()))
}
func (s *Store) ListNotificationLogs(ctx context.Context) ([]NotificationLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,outbox_id,attempt,state,message,created_at FROM notification_logs ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationLog{}
	for rows.Next() {
		var v NotificationLog
		if err = rows.Scan(&v.ID, &v.OutboxID, &v.Attempt, &v.State, &v.Message, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
