package store

import "context"

// Prefer per-attempt evidence; legacy and summary message records remain valid
// only when they do not have a replacement ledger. Never count both.
const aiAccountingCTE = `WITH ai_accounting AS (
 SELECT m.conversation_id,m.served_model_id,m.tokens_in,m.tokens_out,m.cost,m.price_snapshot,m.created_at
 FROM ai_messages m
 WHERE m.role='assistant' AND m.estimated_input>0
 AND NOT EXISTS (SELECT 1 FROM ai_calls a WHERE a.message_id=m.id)
 UNION ALL
 SELECT m.conversation_id,a.served_model_id,a.tokens_in,a.tokens_out,a.cost,a.price_snapshot,a.created_at
 FROM ai_calls a JOIN ai_messages m ON m.id=a.message_id
) `

// AICall records one provider attempt, independently of the assistant message.
// Interrupted rows are written before dispatch so a crash cannot erase the attempt.
type AICall struct {
	ID             int64    `json:"id"`
	MessageID      int64    `json:"messageId"`
	Attempt        int      `json:"attempt"`
	ToolRound      int      `json:"toolRound"`
	ProviderID     string   `json:"providerId"`
	ServedModelID  string   `json:"servedModelId"`
	ProviderModel  string   `json:"providerModel"`
	Status         string   `json:"status"`
	FinishReason   string   `json:"finishReason"`
	TokensIn       *int     `json:"tokensIn"`
	TokensOut      *int     `json:"tokensOut"`
	Cost           *float64 `json:"cost"`
	PriceSnapshot  string   `json:"priceSnapshot"`
	EstimatedInput int      `json:"estimatedInput"`
	CreatedAt      string   `json:"createdAt"`
	FinishedAt     *string  `json:"finishedAt"`
}

func (s *Store) StartAICall(ctx context.Context, v AICall) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO ai_calls(message_id,attempt,tool_round,provider_id,served_model_id,provider_model,status,price_snapshot,estimated_input,created_at) VALUES(?,?,?,?,?,?,'interrupted',?,?,?)`, v.MessageID, v.Attempt, v.ToolRound, v.ProviderID, v.ServedModelID, v.ProviderModel, v.PriceSnapshot, v.EstimatedInput, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) FinishAICall(ctx context.Context, v AICall) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_calls SET status=?,finish_reason=?,tokens_in=?,tokens_out=?,cost=?,finished_at=? WHERE id=?`, v.Status, v.FinishReason, v.TokensIn, v.TokensOut, v.Cost, now(), v.ID)
	return err
}
func (s *Store) AICalls(ctx context.Context, owner, conversation string) ([]AICall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.message_id,a.attempt,a.tool_round,a.provider_id,a.served_model_id,a.provider_model,a.status,a.finish_reason,a.tokens_in,a.tokens_out,a.cost,a.price_snapshot,a.estimated_input,a.created_at,a.finished_at FROM ai_calls a JOIN ai_messages m ON m.id=a.message_id JOIN ai_conversations c ON c.id=m.conversation_id WHERE c.owner_id=? AND c.id=? ORDER BY a.id`, owner, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AICall{}
	for rows.Next() {
		var v AICall
		if err := rows.Scan(&v.ID, &v.MessageID, &v.Attempt, &v.ToolRound, &v.ProviderID, &v.ServedModelID, &v.ProviderModel, &v.Status, &v.FinishReason, &v.TokensIn, &v.TokensOut, &v.Cost, &v.PriceSnapshot, &v.EstimatedInput, &v.CreatedAt, &v.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
