package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

// CredentialAccessRevision binds approval to the encrypted credential and its
// username/kind without decrypting or exposing credential material.
func (s *Store) CredentialAccessRevision(ctx context.Context, id string) (string, error) {
	var sealed []byte
	var username, kind string
	err := s.db.QueryRowContext(ctx, `SELECT secret_ct,username,kind FROM credentials WHERE id=?`, id).Scan(&sealed, &username, &kind)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write(sealed)
	hash.Write([]byte("\x00" + username + "\x00" + kind))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type AccessSession struct {
	ID                string `json:"id"`
	AssetID           string `json:"assetId"`
	Actor             string `json:"actor"`
	OwnerSession      string `json:"-"`
	StartedAt         string `json:"startedAt"`
	EndedAt           string `json:"endedAt"`
	RecordingPurgedAt string `json:"recordingPurgedAt"`
	Result            string `json:"result"`
	BytesIn           int64  `json:"bytesIn"`
	BytesOut          int64  `json:"bytesOut"`
}

func (s *Store) CreateAccessSession(ctx context.Context, assetID, actor, owner string) (*AccessSession, error) {
	a := &AccessSession{ID: newID("term"), AssetID: assetID, Actor: actor, OwnerSession: owner, StartedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id,asset_id,actor,kind,started_at,owner_session) VALUES(?,?,?,'terminal',?,?)`, a.ID, assetID, actor, a.StartedAt, owner)
	return a, err
}
func (s *Store) ListAccessSessions(ctx context.Context) ([]AccessSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(asset_id,''),actor,owner_session,started_at,COALESCE(ended_at,''),result,bytes_in,bytes_out,recording_purged_at FROM sessions WHERE kind='terminal' ORDER BY started_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessSession{}
	for rows.Next() {
		var a AccessSession
		if err := rows.Scan(&a.ID, &a.AssetID, &a.Actor, &a.OwnerSession, &a.StartedAt, &a.EndedAt, &a.Result, &a.BytesIn, &a.BytesOut, &a.RecordingPurgedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) GetAccessSession(ctx context.Context, id string) (*AccessSession, error) {
	var a AccessSession
	err := s.db.QueryRowContext(ctx, `SELECT id,COALESCE(asset_id,''),actor,owner_session,started_at,COALESCE(ended_at,''),result,bytes_in,bytes_out,recording_purged_at FROM sessions WHERE kind='terminal' AND id=?`, id).Scan(&a.ID, &a.AssetID, &a.Actor, &a.OwnerSession, &a.StartedAt, &a.EndedAt, &a.Result, &a.BytesIn, &a.BytesOut, &a.RecordingPurgedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return &a, err
}
func (s *Store) EndAccessSession(ctx context.Context, id, result string, in, out int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at=?,result=?,bytes_in=?,bytes_out=? WHERE id=? AND ended_at IS NULL`, now(), result, in, out, id)
	return err
}
func (s *Store) RecordAccessOutput(ctx context.Context, id string, elapsed float64, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_recordings(session_id,elapsed,data) VALUES(?,?,?)`, id, elapsed, data)
	return err
}
func (s *Store) RecoverAccessSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET ended_at=?,result='interrupted' WHERE kind='terminal' AND ended_at IS NULL`, now())
	return err
}
func (s *Store) RecordAccessResize(ctx context.Context, id string, elapsed float64, cols, rows int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_recordings(session_id,elapsed,data,event_type) VALUES(?,?,?,'r')`, id, elapsed, []byte(fmt.Sprintf("%dx%d", cols, rows)))
	return err
}
func (s *Store) WriteAccessRecording(ctx context.Context, id string, w io.Writer) error {
	session, err := s.GetAccessSession(ctx, id)
	if err != nil {
		return err
	}
	started, _ := time.Parse(time.RFC3339, session.StartedAt)
	enc := json.NewEncoder(w)
	if err := enc.Encode(map[string]any{"version": 2, "width": 120, "height": 30, "timestamp": started.Unix(), "env": map[string]string{"TERM": "xterm"}}); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT elapsed,data,event_type FROM access_recordings WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	var pending []byte
	var lastTime float64
	for rows.Next() {
		var t float64
		var b []byte
		var kind string
		if err := rows.Scan(&t, &b, &kind); err != nil {
			return err
		}
		if kind == "o" {
			b = append(pending, b...)
			end := len(b)
			// Preserve incomplete UTF-8 codepoints across SSH output chunks.
			for start := len(b) - 1; start >= 0 && start >= len(b)-4; start-- {
				if utf8.RuneStart(b[start]) {
					if !utf8.FullRune(b[start:]) {
						end = start
					}
					break
				}
			}
			pending = append([]byte(nil), b[end:]...)
			b = b[:end]
			lastTime = t
		}
		if len(b) > 0 {
			if err := enc.Encode([]any{t, kind, string(b)}); err != nil {
				return err
			}
		}
	}
	if len(pending) > 0 {
		if err := enc.Encode([]any{lastTime, "o", string(pending)}); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *Store) SaveAssetCheck(ctx context.Context, id, device string, ok bool, latency int, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset_checks(asset_id,device_id,ok,latency_ms,error,checked_at) VALUES(?,?,?,?,?,?)`, id, device, b2i(ok), latency, message, now())
	return err
}
func (s *Store) PinAssetHostKey(ctx context.Context, a *Asset, pin string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE assets SET host_key=?,updated_at=? WHERE id=? AND updated_at=? AND address=? AND port=? AND COALESCE(via_asset_id,'')=? AND COALESCE(via_device_id,'')=?`, pin, now(), a.ID, a.UpdatedAt.UTC().Format(time.RFC3339Nano), a.Address, a.Port, a.ViaAssetID, a.ViaDeviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
