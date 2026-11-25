package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type AIProvider struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"baseUrl"`
	CredentialID string `json:"credentialId"`
	Enabled      bool   `json:"enabled"`
}
type AIModel struct {
	ID              string   `json:"id"`
	ProviderID      string   `json:"providerId"`
	ModelID         string   `json:"modelId"`
	Name            string   `json:"name"`
	ContextWindow   int      `json:"contextWindow"`
	MaxOutput       int      `json:"maxOutput"`
	SupportsTools   bool     `json:"supportsTools"`
	FallbackModelID string   `json:"fallbackModelId"`
	InputPrice      *float64 `json:"inputPrice"`
	OutputPrice     *float64 `json:"outputPrice"`
	Enabled         bool     `json:"enabled"`
}
type AIPrompt struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SystemPrompt string `json:"systemPrompt"`
	Version      int    `json:"version"`
}
type AIConversation struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	ModelID        string `json:"modelId"`
	PromptID       string `json:"promptId"`
	PromptVersion  int    `json:"promptVersion"`
	PromptSnapshot string `json:"-"`
	CreatedAt      string `json:"createdAt"`
}
type AIMessage struct {
	ID              int64    `json:"id"`
	Role            string   `json:"role"`
	Content         string   `json:"content"`
	Status          string   `json:"status"`
	TokensIn        *int     `json:"tokensIn"`
	TokensOut       *int     `json:"tokensOut"`
	EstimatedInput  int      `json:"estimatedInput"`
	DroppedMessages int      `json:"droppedMessages"`
	ServedModelID   string   `json:"servedModelId"`
	Cost            *float64 `json:"cost"`
	PriceSnapshot   string   `json:"priceSnapshot"`
	CreatedAt       string   `json:"createdAt"`
}

func (s *Store) ListAIConfigs(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM ai_configs WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}
func (s *Store) GetAIConfig(ctx context.Context, kind, id string, dst any) error {
	var b string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM ai_configs WHERE kind=? AND id=?`, kind, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), dst)
}
func (s *Store) SaveAIConfig(ctx context.Context, kind, id, credential, parent string, value any, create bool) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var cred, par any
	if credential != "" {
		cred = credential
	}
	if parent != "" {
		par = parent
	}
	if kind == "prompts" {
		var prompt AIPrompt
		if err := json.Unmarshal(b, &prompt); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if create {
			_, err = tx.ExecContext(ctx, `INSERT INTO ai_configs(id,kind,body) VALUES(?,?,?)`, id, kind, string(b))
		} else {
			var res sql.Result
			res, err = tx.ExecContext(ctx, `UPDATE ai_configs SET body=? WHERE id=? AND kind='prompts' AND json_extract(body,'$.version')=?`, string(b), id, prompt.Version-1)
			if err == nil {
				n, _ := res.RowsAffected()
				if n == 0 {
					return ErrInUse
				}
			}
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ai_prompt_versions(prompt_id,version,body,created_at) VALUES(?,?,?,?)`, id, prompt.Version, string(b), now()); err != nil {
			return err
		}
		return tx.Commit()
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO ai_configs(id,kind,body,credential_id,parent_id) VALUES(?,?,?,?,?)`, id, kind, string(b), cred, par)
	} else {
		var res sql.Result
		res, err = s.db.ExecContext(ctx, `UPDATE ai_configs SET body=?,credential_id=?,parent_id=? WHERE id=? AND kind=?`, string(b), cred, par, id, kind)
		if err == nil {
			n, _ := res.RowsAffected()
			if n == 0 {
				return ErrNotFound
			}
		}
	}
	return translateConstraint(err)
}
func (s *Store) AIPromptVersions(ctx context.Context, id string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM ai_prompt_versions WHERE prompt_id=? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}
func (s *Store) DeleteAIConfig(ctx context.Context, kind, id string) error {
	if kind == "models" {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_configs WHERE kind='models' AND json_extract(body,'$.fallbackModelId')=?`, id).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrInUse
		}
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM ai_configs WHERE kind=? AND id=?`, kind, id)
	if isForeignKeyError(err) {
		return ErrInUse
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) CreateAIConversation(ctx context.Context, owner string, v AIConversation) error {
	var prompt any
	if v.PromptID != "" {
		prompt = v.PromptID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_conversations(id,owner_id,title,model_id,prompt_id,prompt_version,prompt_snapshot,created_at) VALUES(?,?,?,?,?,?,?,?)`, v.ID, owner, v.Title, v.ModelID, prompt, v.PromptVersion, v.PromptSnapshot, now())
	return translateConstraint(err)
}
func (s *Store) ListAIConversations(ctx context.Context, owner string) ([]AIConversation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,model_id,COALESCE(prompt_id,''),prompt_version,prompt_snapshot,created_at FROM ai_conversations WHERE owner_id=? ORDER BY created_at DESC LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIConversation{}
	for rows.Next() {
		var v AIConversation
		if err := rows.Scan(&v.ID, &v.Title, &v.ModelID, &v.PromptID, &v.PromptVersion, &v.PromptSnapshot, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) GetAIConversation(ctx context.Context, owner, id string) (AIConversation, error) {
	var v AIConversation
	err := s.db.QueryRowContext(ctx, `SELECT id,title,model_id,COALESCE(prompt_id,''),prompt_version,prompt_snapshot,created_at FROM ai_conversations WHERE owner_id=? AND id=?`, owner, id).Scan(&v.ID, &v.Title, &v.ModelID, &v.PromptID, &v.PromptVersion, &v.PromptSnapshot, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func (s *Store) AIMessages(ctx context.Context, id string) ([]AIMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,role,content,status,tokens_in,tokens_out,estimated_input,dropped_messages,served_model_id,cost,price_snapshot,created_at FROM ai_messages WHERE conversation_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIMessage{}
	for rows.Next() {
		var v AIMessage
		if err := rows.Scan(&v.ID, &v.Role, &v.Content, &v.Status, &v.TokensIn, &v.TokensOut, &v.EstimatedInput, &v.DroppedMessages, &v.ServedModelID, &v.Cost, &v.PriceSnapshot, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AddAIMessage(ctx context.Context, id string, v AIMessage) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO ai_messages(conversation_id,role,content,status,tokens_in,tokens_out,estimated_input,dropped_messages,served_model_id,cost,price_snapshot,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, v.Role, v.Content, v.Status, v.TokensIn, v.TokensOut, v.EstimatedInput, v.DroppedMessages, v.ServedModelID, v.Cost, v.PriceSnapshot, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) FinishAIMessage(ctx context.Context, id int64, v AIMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_messages SET content=?,status=?,tokens_in=?,tokens_out=?,served_model_id=?,cost=?,price_snapshot=?,estimated_input=?,dropped_messages=? WHERE id=?`, v.Content, v.Status, v.TokensIn, v.TokensOut, v.ServedModelID, v.Cost, v.PriceSnapshot, v.EstimatedInput, v.DroppedMessages, id)
	return err
}
func (s *Store) AIUsage(ctx context.Context, owner string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, aiAccountingCTE+`SELECT m.served_model_id,COUNT(*),SUM(CASE WHEN m.tokens_in IS NOT NULL AND m.tokens_out IS NOT NULL THEN 1 ELSE 0 END),SUM(m.tokens_in),SUM(m.tokens_out),SUM(m.cost),COUNT(m.cost) FROM ai_accounting m JOIN ai_conversations c ON c.id=m.conversation_id WHERE c.owner_id=? GROUP BY m.served_model_id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var model string
		var calls, reported, priced int
		var in, outTokens *int64
		var cost *float64
		if err := rows.Scan(&model, &calls, &reported, &in, &outTokens, &cost, &priced); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"modelId": model, "requests": calls, "reportedRequests": reported, "tokensIn": in, "tokensOut": outTokens, "estimatedCost": cost, "pricedRequests": priced, "unknownUsageRequests": calls - reported, "unknownCostRequests": calls - priced, "currency": "USD"})
	}
	return out, rows.Err()
}

// AIUsageDay totals persisted generation records; it never reprices history from
// the current model configuration. Unknown model IDs remain empty rather than
// being attributed to the conversation's requested model after a fallback.
type AIUsageDay struct {
	Day                  string   `json:"day"`
	ModelID              string   `json:"modelId"`
	Requests             int64    `json:"requests"`
	ReportedRequests     int64    `json:"reportedRequests"`
	UnknownUsageRequests int64    `json:"unknownUsageRequests"`
	TokensIn             *int64   `json:"tokensIn"`
	TokensOut            *int64   `json:"tokensOut"`
	EstimatedCost        *float64 `json:"estimatedCost"`
	PricedRequests       int64    `json:"pricedRequests"`
	UnknownCostRequests  int64    `json:"unknownCostRequests"`
	UnknownPriceRequests int64    `json:"unknownPriceRequests"`
	Currency             string   `json:"currency"`
}

// AIUsageDaily includes today and the previous days-1 UTC calendar days. Empty
// days are omitted; no-output, failed and interrupted generation rows still count.
func (s *Store) AIUsageDaily(ctx context.Context, owner string, days int, at time.Time) ([]AIUsageDay, error) {
	if days < 1 || days > 366 {
		return nil, errors.New("AI usage days must be between 1 and 366")
	}
	utc := at.UTC()
	end := time.Date(utc.Year(), utc.Month(), utc.Day()+1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -days)
	rows, err := s.db.QueryContext(ctx, aiAccountingCTE+`SELECT date(m.created_at), m.served_model_id,
		COUNT(*), SUM(CASE WHEN m.tokens_in IS NOT NULL AND m.tokens_out IS NOT NULL THEN 1 ELSE 0 END),
		SUM(m.tokens_in), SUM(m.tokens_out), SUM(m.cost), COUNT(m.cost),
		SUM(CASE WHEN m.cost IS NOT NULL THEN 0
			WHEN json_valid(m.price_snapshot) THEN CASE WHEN
				json_type(m.price_snapshot,'$.inputPerMillion') IN ('integer','real') AND
				json_type(m.price_snapshot,'$.outputPerMillion') IN ('integer','real')
				THEN 0 ELSE 1 END
			ELSE 1 END)
		FROM ai_accounting m JOIN ai_conversations c ON c.id=m.conversation_id
		WHERE c.owner_id=?
		AND julianday(m.created_at)>=julianday(?) AND julianday(m.created_at)<julianday(?)
		GROUP BY date(m.created_at), m.served_model_id
		ORDER BY date(m.created_at) DESC, m.served_model_id`, owner, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIUsageDay{}
	for rows.Next() {
		v := AIUsageDay{Currency: "USD"}
		if err := rows.Scan(&v.Day, &v.ModelID, &v.Requests, &v.ReportedRequests, &v.TokensIn, &v.TokensOut, &v.EstimatedCost, &v.PricedRequests, &v.UnknownPriceRequests); err != nil {
			return nil, err
		}
		v.UnknownUsageRequests = v.Requests - v.ReportedRequests
		v.UnknownCostRequests = v.Requests - v.PricedRequests
		out = append(out, v)
	}
	return out, rows.Err()
}

type AIProposal struct {
	Args           json.RawMessage `json:"args,omitempty"`
	ApprovedBy     string          `json:"approvedBy,omitempty"`
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	Tool           string          `json:"tool"`
	Target         string          `json:"target"`
	Revision       int64           `json:"revision"`
	Reason         string          `json:"reason"`
	Risk           string          `json:"risk"`
	Diff           string          `json:"diff"`
	Impact         string          `json:"impact"`
	Rollback       string          `json:"rollback"`
	Verification   string          `json:"verification"`
	Status         string          `json:"status"`
	Result         string          `json:"result"`
}

func (s *Store) CreateAIProposal(ctx context.Context, p AIProposal) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_proposals(id,conversation_id,body,status,created_at) VALUES(?,?,?,?,?)`, p.ID, p.ConversationID, string(b), "proposed", now())
	return err
}
func (s *Store) ListAIProposals(ctx context.Context, owner string) ([]AIProposal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.body,p.status,p.result FROM ai_proposals p JOIN ai_conversations c ON c.id=p.conversation_id WHERE c.owner_id=? ORDER BY p.created_at DESC LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIProposal{}
	for rows.Next() {
		var b, status, result string
		if err := rows.Scan(&b, &status, &result); err != nil {
			return nil, err
		}
		var p AIProposal
		if err := json.Unmarshal([]byte(b), &p); err != nil {
			return nil, err
		}
		p.Status = status
		p.Result = result
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) GetAIProposal(ctx context.Context, owner, id string) (AIProposal, error) {
	var p AIProposal
	var body, status, result, approved string
	err := s.db.QueryRowContext(ctx, `SELECT p.body,p.status,p.result,COALESCE(p.approved_by,'') FROM ai_proposals p JOIN ai_conversations c ON c.id=p.conversation_id WHERE c.owner_id=? AND p.id=?`, owner, id).Scan(&body, &status, &result, &approved)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return p, err
	}
	p.Status = status
	p.Result = result
	p.ApprovedBy = approved
	return p, nil
}
func (s *Store) ClaimAIProposal(ctx context.Context, owner, id, status string) (AIProposal, error) {
	var p AIProposal
	var b string
	err := s.db.QueryRowContext(ctx, `SELECT p.body FROM ai_proposals p JOIN ai_conversations c ON c.id=p.conversation_id WHERE c.owner_id=? AND p.id=?`, owner, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(b), &p); err != nil {
		return p, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE ai_proposals SET status=?,approved_by=? WHERE id=? AND status='proposed'`, status, owner, id)
	if err != nil {
		return p, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return p, ErrInUse
	}
	p.Status = status
	p.ApprovedBy = owner
	return p, nil
}
func (s *Store) FinishAIProposal(ctx context.Context, id, status, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_proposals SET status=?,result=? WHERE id=? AND status='executing'`, status, result, id)
	return err
}
