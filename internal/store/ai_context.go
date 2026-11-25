package store

import (
	"context"
	"database/sql"
	"errors"
)

type AITaskRoute struct {
	Task    string `json:"task"`
	ModelID string `json:"modelId"`
}
type AISummary struct {
	Content        string `json:"content"`
	ThroughMessage int64  `json:"throughMessage"`
	UpdatedAt      string `json:"updatedAt"`
}

func (s *Store) AISummary(ctx context.Context, owner, conversation string) (AISummary, error) {
	if _, err := s.GetAIConversation(ctx, owner, conversation); err != nil {
		return AISummary{}, err
	}
	var v AISummary
	err := s.db.QueryRowContext(ctx, `SELECT content,through_message,updated_at FROM ai_conversation_summaries WHERE conversation_id=?`, conversation).Scan(&v.Content, &v.ThroughMessage, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	return v, err
}
func (s *Store) SaveAISummary(ctx context.Context, owner, conversation string, v AISummary) error {
	if _, err := s.GetAIConversation(ctx, owner, conversation); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_conversation_summaries(conversation_id,content,through_message,updated_at) VALUES(?,?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET content=excluded.content,through_message=excluded.through_message,updated_at=excluded.updated_at`, conversation, v.Content, v.ThroughMessage, now())
	return err
}

type AIFacts struct {
	Content   string `json:"content"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updatedAt"`
}

func (s *Store) AITaskRoutes(ctx context.Context, owner string) ([]AITaskRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task,model_id FROM ai_task_routes WHERE owner_id=? ORDER BY task`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AITaskRoute{}
	for rows.Next() {
		var v AITaskRoute
		if err = rows.Scan(&v.Task, &v.ModelID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveAITaskRoute(ctx context.Context, owner string, v AITaskRoute) error {
	if v.ModelID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM ai_task_routes WHERE owner_id=? AND task=?`, owner, v.Task)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_task_routes(owner_id,task,model_id) VALUES(?,?,?) ON CONFLICT(owner_id,task) DO UPDATE SET model_id=excluded.model_id`, owner, v.Task, v.ModelID)
	return translateConstraint(err)
}
func (s *Store) ResolveAITask(ctx context.Context, owner, task string) (string, error) {
	var model string
	err := s.db.QueryRowContext(ctx, `SELECT model_id FROM ai_task_routes WHERE owner_id=? AND task=?`, owner, task).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return model, err
}
func (s *Store) AIFacts(ctx context.Context, owner, conversation string) (AIFacts, error) {
	if _, err := s.GetAIConversation(ctx, owner, conversation); err != nil {
		return AIFacts{}, err
	}
	var v AIFacts
	err := s.db.QueryRowContext(ctx, `SELECT content,revision,updated_at FROM ai_conversation_facts WHERE conversation_id=?`, conversation).Scan(&v.Content, &v.Revision, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	return v, err
}
func (s *Store) SaveAIFacts(ctx context.Context, owner, conversation string, v AIFacts) error {
	if _, err := s.GetAIConversation(ctx, owner, conversation); err != nil {
		return err
	}
	var result sql.Result
	var err error
	if v.Revision == 0 {
		result, err = s.db.ExecContext(ctx, `INSERT INTO ai_conversation_facts(conversation_id,content,revision,updated_at) VALUES(?,?,1,?) ON CONFLICT(conversation_id) DO NOTHING`, conversation, v.Content, now())
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE ai_conversation_facts SET content=?,revision=revision+1,updated_at=? WHERE conversation_id=? AND revision=?`, v.Content, now(), conversation, v.Revision)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInUse
	}
	return nil
}
