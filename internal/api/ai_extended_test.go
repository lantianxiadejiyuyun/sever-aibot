package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"server-aibot/internal/store"
)

func TestAIFallbackPricingAndPartialOutput(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			var fallbackCalls atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fallbackCalls.Add(1)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
			}))
			defer fallback.Close()
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if partial {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
					return
				}
				w.WriteHeader(503)
			}))
			defer primary.Close()
			env := newTestEnv(t)
			id := seedAI(t, env, primary.URL)
			ctx := context.Background()
			p := store.AIProvider{ID: "fallback_provider", Name: "fallback", Kind: "openai_compatible", BaseURL: fallback.URL, Enabled: true}
			price := 2.0
			m := store.AIModel{ID: "fallback_model", ProviderID: p.ID, Name: "fallback", ModelID: "fake", ContextWindow: 4096, MaxOutput: 100, Enabled: true, InputPrice: &price, OutputPrice: &price}
			if err := env.store.SaveAIConfig(ctx, "providers", p.ID, "", "", p, true); err != nil {
				t.Fatal(err)
			}
			if err := env.store.SaveAIConfig(ctx, "models", m.ID, "", p.ID, m, true); err != nil {
				t.Fatal(err)
			}
			var initial store.AIModel
			if err := env.store.GetAIConfig(ctx, "models", "ai_model", &initial); err != nil {
				t.Fatal(err)
			}
			initial.FallbackModelID = m.ID
			if err := env.store.SaveAIConfig(ctx, "models", initial.ID, "", initial.ProviderID, initial, false); err != nil {
				t.Fatal(err)
			}
			response := env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/messages", map[string]any{"content": "hello"}, true)
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			rows, err := env.store.AIMessages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			last := rows[len(rows)-1]
			if partial {
				if fallbackCalls.Load() != 0 || last.Content != "partial" || !strings.Contains(string(body), "event: error") {
					t.Fatalf("partial fallback: %s %+v", body, last)
				}
			} else {
				if fallbackCalls.Load() != 1 || last.ServedModelID != m.ID || last.Cost == nil || *last.Cost <= 0 || !strings.Contains(string(body), "event: fallback") {
					t.Fatalf("fallback: %s %+v", body, last)
				}
				daily := decode[dailyUsageResponse](t, env.do(t, "GET", "/api/v1/ai/usage/daily?days=1", nil, false))
				if len(daily.Items) != 2 || daily.Items[0].ModelID != initial.ID || daily.Items[0].UnknownUsageRequests != 1 || daily.Items[1].ModelID != m.ID || daily.Items[1].EstimatedCost == nil || *daily.Items[1].EstimatedCost != *last.Cost {
					t.Fatalf("daily usage lost actual fallback model or price: %+v", daily)
				}
			}
		})
	}
}

func TestAIReadToolContinuation(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(fmt.Sprint(authorized), func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var input struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				if calls.Add(1) == 1 {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"list_devices\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				last := input.Messages[len(input.Messages)-1].Content
				if !strings.Contains(last, "untrusted_tool_results") || (!authorized && !strings.Contains(last, "not authorized")) {
					t.Errorf("missing bounded tool context: %s", last)
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			env := newTestEnv(t)
			id := seedAI(t, env, provider.URL)
			ctx := context.Background()
			var m store.AIModel
			if err := env.store.GetAIConfig(ctx, "models", "ai_model", &m); err != nil {
				t.Fatal(err)
			}
			m.SupportsTools = true
			m.ContextWindow = 64000
			if err := env.store.SaveAIConfig(ctx, "models", m.ID, "", m.ProviderID, m, false); err != nil {
				t.Fatal(err)
			}
			response := env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/messages", map[string]any{"content": "list devices", "includeContext": authorized}, true)
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if calls.Load() != 2 || !strings.Contains(string(body), "event: tool_result") || !strings.Contains(string(body), "event: done") {
				t.Fatalf("bad loop %s", body)
			}
			rows, err := env.store.AIMessages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.Role == "tool" {
					found = true
				}
			}
			if !found {
				t.Fatal("tool result not persisted")
			}
		})
	}
}
