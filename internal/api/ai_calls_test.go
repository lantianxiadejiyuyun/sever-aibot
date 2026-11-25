package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/store"
)

func TestAIAccountingFallbackAndToolRounds(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 2 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"list_devices","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":0}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	e := newTestEnv(t)
	conv := seedAI(t, e, provider.URL)
	ctx := context.Background()
	var m store.AIModel
	if err := e.store.GetAIConfig(ctx, "models", "ai_model", &m); err != nil {
		t.Fatal(err)
	}
	fallback := m
	fallback.ID = "fallback_accounting"
	fallback.ModelID = "actual-provider-model"
	fallback.SupportsTools = true
	fallback.ContextWindow = 32000
	rate := 2.0
	fallback.InputPrice = &rate
	fallback.OutputPrice = &rate
	if err := e.store.SaveAIConfig(ctx, "models", fallback.ID, "", fallback.ProviderID, fallback, true); err != nil {
		t.Fatal(err)
	}
	m.FallbackModelID = fallback.ID
	if err := e.store.SaveAIConfig(ctx, "models", m.ID, "", m.ProviderID, m, false); err != nil {
		t.Fatal(err)
	}
	res := e.do(t, "POST", "/api/v1/ai/conversations/"+conv+"/messages", map[string]any{"content": "hello"}, true)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if requests.Load() != 3 {
		t.Fatalf("calls=%d body=%s", requests.Load(), body)
	}
	res = e.do(t, "GET", "/api/v1/ai/conversations/"+conv, nil, false)
	var detail struct {
		Calls []store.AICall `json:"calls"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	calls := detail.Calls
	if len(calls) != 3 {
		t.Fatalf("calls=%+v", calls)
	}
	if calls[0].Status != "error" || calls[0].TokensIn != nil || calls[0].Cost != nil || calls[0].ServedModelID != m.ID {
		t.Fatalf("failed attempt=%+v", calls[0])
	}
	if calls[1].ToolRound != 0 || calls[1].Attempt != 2 || calls[1].ProviderModel != fallback.ModelID || calls[1].TokensIn == nil || *calls[1].TokensIn != 10 || calls[1].Cost == nil || math.Abs(*calls[1].Cost-0.00002) > 1e-12 {
		t.Fatalf("tool request=%+v", calls[1])
	}
	if calls[2].ToolRound != 1 || calls[2].Attempt != 3 || calls[2].TokensIn != nil || calls[2].Cost != nil || calls[2].Status != "completed" {
		t.Fatalf("continuation=%+v", calls[2])
	}
	// Use authenticated routes rather than assuming the fixture owner's ID.
	res = e.do(t, "GET", "/api/v1/ai/usage/daily?days=1", nil, false)
	daily := decode[dailyUsageResponse](t, res)
	if len(daily.Items) != 2 {
		t.Fatalf("daily=%+v", daily)
	}
	for _, row := range daily.Items {
		if row.ModelID == fallback.ID && (row.Requests != 2 || row.ReportedRequests != 1 || row.UnknownUsageRequests != 1 || row.TokensIn == nil || *row.TokensIn != 10 || row.EstimatedCost == nil || math.Abs(*row.EstimatedCost-0.00002) > 1e-12) {
			t.Fatalf("partial subtotal=%+v", row)
		}
	}
	res = e.do(t, "GET", "/api/v1/ai/usage", nil, false)
	var cumulative struct {
		Items []struct {
			Requests int  `json:"requests"`
			TokensIn *int `json:"tokensIn"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&cumulative); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	total := 0
	for _, row := range cumulative.Items {
		total += row.Requests
	}
	if total != 3 {
		t.Fatalf("double counted: %+v", cumulative)
	}
	hidden, err := e.store.AICalls(ctx, "other-owner", conv)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("owner leak: %+v %v", hidden, err)
	}
}

func TestAIAccountingFailedStreamRetainsReportedUsage(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":0}}\n\n")
		// Missing DONE leaves an error after usage was already reported.
	}))
	defer provider.Close()
	e := newTestEnv(t)
	conv := seedAI(t, e, provider.URL)
	res := e.do(t, "POST", "/api/v1/ai/conversations/"+conv+"/messages", map[string]string{"content": "hello"}, true)
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	res = e.do(t, "GET", "/api/v1/ai/conversations/"+conv, nil, false)
	var detail struct {
		Calls []store.AICall `json:"calls"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(detail.Calls) != 1 || detail.Calls[0].Status != "error" || detail.Calls[0].TokensIn == nil || *detail.Calls[0].TokensIn != 7 || detail.Calls[0].TokensOut == nil || *detail.Calls[0].TokensOut != 0 || detail.Calls[0].Cost != nil {
		t.Fatalf("failed reported usage=%+v", detail.Calls)
	}
}

func TestAIAccountingCancelledAttempt(t *testing.T) {
	started := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer provider.Close()
	e := newTestEnv(t)
	conv := seedAI(t, e, provider.URL)
	res := e.do(t, "POST", "/api/v1/ai/conversations/"+conv+"/messages", map[string]string{"content": "hello"}, true)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	var status string
	var tokens *int
	if err := e.store.DB().QueryRow(`SELECT status,tokens_in FROM ai_calls`).Scan(&status, &tokens); err != nil || status != "interrupted" || tokens != nil {
		t.Fatalf("attempt must persist before provider completes: %s %v %v", status, tokens, err)
	}
	cancelled := e.do(t, "POST", "/api/v1/ai/conversations/"+conv+"/cancel", map[string]any{}, true)
	cancelled.Body.Close()
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	res = e.do(t, "GET", "/api/v1/ai/conversations/"+conv, nil, false)
	var detail struct {
		Calls []store.AICall `json:"calls"`
	}
	if err := json.NewDecoder(res.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(detail.Calls) != 1 || detail.Calls[0].Status != "cancelled" || detail.Calls[0].TokensIn != nil || detail.Calls[0].Cost != nil || detail.Calls[0].FinishedAt == nil {
		t.Fatalf("cancel evidence=%+v", detail.Calls)
	}
}
