package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrChangeState = errors.New("change is expired, already claimed, or revision does not match")

type ChangeProposal struct {
	ID            string          `json:"id"`
	AssetID       string          `json:"assetId"`
	Kind          string          `json:"kind"`
	Service       string          `json:"service"`
	Summary       string          `json:"summary"`
	Actor         string          `json:"actor"`
	Status        string          `json:"status"`
	Revision      string          `json:"revision"`
	Plan          json.RawMessage `json:"plan"`
	Diff          string          `json:"diff"`
	Impact        string          `json:"impact"`
	Risk          string          `json:"risk"`
	Rollback      string          `json:"rollback"`
	Verification  string          `json:"verification"`
	ApprovedBy    string          `json:"approvedBy"`
	ApprovedUntil string          `json:"approvedUntil"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
}
type ChangeExecution struct {
	ID         string `json:"id"`
	ProposalID string `json:"proposalId"`
	AssetID    string `json:"assetId"`
	Actor      string `json:"actor"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	BackupPath string `json:"backupPath"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt"`
}
type DeploymentArchive struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	ProposalID string          `json:"proposalId"`
	AssetID    string          `json:"assetId"`
	Plan       json.RawMessage `json:"plan"`
	Digest     string          `json:"digest"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  string          `json:"createdAt"`
}

const changeCols = `id,asset_id,kind,service,summary,actor,status,revision,plan,diff,impact,risk,rollback,verification,approved_by,COALESCE(approved_until,''),created_at,updated_at`

func scanChange(row scanner) (*ChangeProposal, error) {
	var p ChangeProposal
	var body string
	err := row.Scan(&p.ID, &p.AssetID, &p.Kind, &p.Service, &p.Summary, &p.Actor, &p.Status, &p.Revision, &body, &p.Diff, &p.Impact, &p.Risk, &p.Rollback, &p.Verification, &p.ApprovedBy, &p.ApprovedUntil, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	p.Plan = json.RawMessage(body)
	return &p, err
}
func (s *Store) GetChangeProposal(ctx context.Context, id string) (*ChangeProposal, error) {
	return scanChange(s.db.QueryRowContext(ctx, `SELECT `+changeCols+` FROM change_proposals WHERE id=?`, id))
}
func (s *Store) ListChangeProposals(ctx context.Context) ([]ChangeProposal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+changeCols+` FROM change_proposals ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeProposal{}
	for rows.Next() {
		p, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *Store) CreateChangeProposal(ctx context.Context, p ChangeProposal) (*ChangeProposal, error) {
	p.ID = newID("chg")
	_, err := s.db.ExecContext(ctx, `INSERT INTO change_proposals(id,asset_id,kind,service,summary,actor,status,revision,plan,diff,impact,risk,rollback,verification,created_at,updated_at) VALUES(?,?,?,?,?,?,'draft',?,?,?,?,?,?,?,?,?)`, p.ID, p.AssetID, p.Kind, p.Service, p.Summary, p.Actor, p.Revision, string(p.Plan), p.Diff, p.Impact, p.Risk, p.Rollback, p.Verification, now(), now())
	if err != nil {
		return nil, err
	}
	return s.GetChangeProposal(ctx, p.ID)
}
func (s *Store) ApproveChange(ctx context.Context, id, revision, actor string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE change_proposals SET status='approved',approved_by=?,approved_until=?,updated_at=? WHERE id=? AND revision=? AND status='draft'`, actor, time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339), now(), id, revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrChangeState
	}
	return nil
}
func (s *Store) ClaimChange(ctx context.Context, id, revision, actor string) (*ChangeExecution, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE change_proposals SET status='running',updated_at=? WHERE id=? AND revision=? AND status='approved' AND approved_by=? AND approved_until>?`, now(), id, revision, actor, now())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrChangeState
	}
	var asset string
	if err = tx.QueryRowContext(ctx, `SELECT asset_id FROM change_proposals WHERE id=?`, id).Scan(&asset); err != nil {
		return nil, err
	}
	run := &ChangeExecution{ID: newID("exec"), ProposalID: id, AssetID: asset, Actor: actor, Status: "running", StartedAt: now()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO change_executions(id,proposal_id,asset_id,actor,status,started_at) VALUES(?,?,?,?,'running',?)`, run.ID, id, asset, actor, run.StartedAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}
func (s *Store) FinishChange(ctx context.Context, run ChangeExecution) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE change_executions SET status=?,output=?,backup_path=?,ended_at=? WHERE id=? AND ended_at IS NULL`, run.Status, run.Output, run.BackupPath, now(), run.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE change_proposals SET status=?,updated_at=? WHERE id=? AND status='running'`, run.Status, now(), run.ProposalID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListChangeExecutions(ctx context.Context, id string) ([]ChangeExecution, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,proposal_id,asset_id,actor,status,output,backup_path,started_at,COALESCE(ended_at,'') FROM change_executions WHERE proposal_id=? ORDER BY started_at DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeExecution{}
	for rows.Next() {
		var e ChangeExecution
		if err = rows.Scan(&e.ID, &e.ProposalID, &e.AssetID, &e.Actor, &e.Status, &e.Output, &e.BackupPath, &e.StartedAt, &e.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) CreateDeploymentArchive(ctx context.Context, a DeploymentArchive) (*DeploymentArchive, error) {
	a.ID = newID("dep")
	a.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_archives(id,name,version,proposal_id,asset_id,plan,digest,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, a.Version, a.ProposalID, a.AssetID, string(a.Plan), a.Digest, a.CreatedBy, a.CreatedAt)
	return &a, err
}
func (s *Store) ListDeploymentArchives(ctx context.Context) ([]DeploymentArchive, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,version,proposal_id,asset_id,plan,digest,created_by,created_at FROM deployment_archives ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeploymentArchive{}
	for rows.Next() {
		var a DeploymentArchive
		var plan string
		if err = rows.Scan(&a.ID, &a.Name, &a.Version, &a.ProposalID, &a.AssetID, &plan, &a.Digest, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Plan = json.RawMessage(plan)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) RecoverChanges(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE change_proposals SET status='interrupted',updated_at=? WHERE status='running'`, now()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE change_executions SET status='interrupted',ended_at=? WHERE ended_at IS NULL`, now()); err != nil {
		return err
	}
	return tx.Commit()
}
