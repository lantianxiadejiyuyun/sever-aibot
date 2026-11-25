package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"server-aibot/internal/ai"
	"server-aibot/internal/store"
)

func (a *aiRuntime) getSummary(w http.ResponseWriter, r *http.Request) {
	conv, ok := a.conversation(w, r)
	if !ok {
		return
	}
	v, err := a.server.store.AISummary(r.Context(), sessionFrom(r.Context()).AdminID, conv.ID)
	if err != nil {
		a.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *aiRuntime) summarize(w http.ResponseWriter, r *http.Request) {
	conv, ok := a.conversation(w, r)
	if !ok {
		return
	}
	var input struct {
		Source  string `json:"source"`
		Logs    string `json:"logs"`
		Consent bool   `json:"consent"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Consent || (input.Source != "logs" && input.Source != "history") {
		writeErr(w, 400, "summary_consent_required", "请选择日志或历史摘要，并确认发送至会话模型（最多 32 次调用）")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	a.mu.Lock()
	if _, exists := a.active[conv.ID]; exists || len(a.active) >= 4 {
		a.mu.Unlock()
		writeErr(w, 409, "ai_busy", "会话正在生成或并发已达上限")
		return
	}
	a.active[conv.ID] = cancel
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.active, conv.ID); a.mu.Unlock() }()
	m, p, key, ok := a.model(w, r, conv.ModelID)
	if !ok {
		return
	}
	logs := input.Logs
	var through int64
	if input.Source == "history" {
		messages, err := a.server.store.AIMessages(ctx, conv.ID)
		if err != nil {
			a.storeErr(w, err)
			return
		}
		var b strings.Builder
		for _, message := range messages {
			if message.Status == "completed" && (message.Role == "user" || message.Role == "assistant") {
				raw, _ := json.Marshal(map[string]any{"id": message.ID, "role": message.Role, "content": message.Content})
				b.Write(raw)
				b.WriteByte('\n')
				through = message.ID
			}
		}
		logs = b.String()
	}
	output := min(m.MaxOutput, 1024)
	chunk := min(16<<10, (m.ContextWindow-output-2048)/6)
	if chunk < 8 {
		writeErr(w, 400, "summary_context_small", "此模型上下文不足以安全归纳，请使用更大上下文模型")
		return
	}
	inputLimit := min(256<<10, chunk*24)
	if !utf8.ValidString(logs) || strings.TrimSpace(logs) == "" || len(logs) > inputLimit {
		writeErr(w, 400, "summary_input_limit", fmt.Sprintf("此模型支持的本次归纳输入为 1–%d 字节", inputLimit))
		return
	}
	req := ai.LogSummaryRequest{Kind: p.Kind, BaseURL: p.BaseURL, APIKey: key, Model: m.ModelID, Logs: logs, ContextWindow: m.ContextWindow, Limits: ai.LogSummaryLimits{InputBytes: inputLimit, ChunkBytes: chunk, SummaryBytes: min(2048, chunk/2), OutputTokens: output, Requests: 32}}
	if m.InputPrice != nil && m.OutputPrice != nil {
		req.Prices = &ai.LogSummaryPrices{InputPerMillion: *m.InputPrice, OutputPerMillion: *m.OutputPrice}
	}
	result, summaryErr := ai.SummarizeLogs(ctx, a.client, req)
	// Persist attempted calls even if the browser cancelled. No raw log text or keys.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer saveCancel()
	for _, call := range result.Calls {
		evidence, _ := json.Marshal(call)
		row := store.AIMessage{Role: "assistant", Status: "summary_call", Content: string(evidence), EstimatedInput: call.EstimatedInput, ServedModelID: m.ID, PriceSnapshot: aiPriceSnapshot(m), Cost: call.EstimatedCost}
		if call.Usage != nil {
			row.TokensIn = &call.Usage.PromptTokens
			row.TokensOut = &call.Usage.CompletionTokens
		}
		if _, err := a.server.store.AddAIMessage(saveCtx, conv.ID, row); err != nil {
			a.storeErr(w, err)
			return
		}
	}
	a.server.audit(r, "ai.summary", conv.ID, map[string]any{"source": input.Source, "calls": len(result.Calls), "ok": summaryErr == nil})
	if summaryErr != nil {
		writeJSON(w, 502, map[string]any{"error": map[string]string{"code": "summary_failed", "message": "归纳未完成；已记录尝试调用的用量，未保存部分摘要"}, "calls": result.Calls})
		return
	}
	if input.Source == "history" {
		if err := a.server.store.SaveAISummary(saveCtx, sessionFrom(r.Context()).AdminID, conv.ID, store.AISummary{Content: result.Summary, ThroughMessage: through}); err != nil {
			a.storeErr(w, err)
			return
		}
	} else {
		if _, err := a.server.store.AddAIMessage(saveCtx, conv.ID, store.AIMessage{Role: "user", Status: "completed", Content: "用户提交日志的分段归纳（原始日志未保存）"}); err != nil {
			a.storeErr(w, err)
			return
		}
		if _, err := a.server.store.AddAIMessage(saveCtx, conv.ID, store.AIMessage{Role: "assistant", Status: "completed", Content: result.Summary}); err != nil {
			a.storeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, result)
}
