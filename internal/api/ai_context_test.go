package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"server-aibot/internal/ai"
	"server-aibot/internal/store"
)

func TestAITaskDefaultsAndFactRevisions(t *testing.T) {
	e := newTestEnv(t)
	id := seedAI(t, e, "http://127.0.0.1:1")
	r := e.do(t, "PUT", "/api/v1/ai/task-routes", map[string]any{"task": "inspection", "modelId": "ai_model"}, true)
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("route bypassed step-up", r.StatusCode)
	}
	r = e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	r = e.do(t, "PUT", "/api/v1/ai/task-routes", map[string]any{"task": "inspection", "modelId": "ai_model"}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	r = e.do(t, "POST", "/api/v1/ai/conversations", map[string]any{"task": "inspection"}, true)
	if r.StatusCode != 201 {
		t.Fatal(r.StatusCode)
	}
	conv := decode[store.AIConversation](t, r)
	if conv.ModelID != "ai_model" {
		t.Fatal(conv)
	}
	r = e.do(t, "POST", "/api/v1/ai/conversations", map[string]any{"task": "change"}, true)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("missing route silently resolved")
	}
	r = e.do(t, "PUT", "/api/v1/ai/conversations/"+id+"/facts", map[string]any{"content": "confirmed fact", "revision": 0}, true)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	facts := decode[store.AIFacts](t, r)
	if facts.Revision != 1 {
		t.Fatal(facts)
	}
	r = e.do(t, "PUT", "/api/v1/ai/conversations/"+id+"/facts", map[string]any{"content": "stale overwrite", "revision": 0}, true)
	r.Body.Close()
	if r.StatusCode != 409 {
		t.Fatal("stale facts overwritten", r.StatusCode)
	}
	if _, err := e.store.AIFacts(context.Background(), "different-owner", id); err == nil {
		t.Fatal("cross-owner facts exposed")
	}
	if err := e.store.SaveAIFacts(context.Background(), "different-owner", id, store.AIFacts{Content: "bad", Revision: 1}); err == nil {
		t.Fatal("cross-owner facts changed")
	}
	r = e.do(t, "PUT", "/api/v1/ai/conversations/"+id+"/facts", map[string]any{"content": "", "revision": 1}, true)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	facts = decode[store.AIFacts](t, r)
	if facts.Content != "" || facts.Revision != 2 {
		t.Fatal(facts)
	}
	var model store.AIModel
	if err := e.store.GetAIConfig(context.Background(), "models", "ai_model", &model); err != nil {
		t.Fatal(err)
	}
	model.Enabled = false
	if err := e.store.SaveAIConfig(context.Background(), "models", model.ID, "", model.ProviderID, model, false); err != nil {
		t.Fatal(err)
	}
	r = e.do(t, "POST", "/api/v1/ai/conversations", map[string]any{"task": "inspection"}, true)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("disabled route accepted", r.StatusCode)
	}
}

func TestAISummaryCallsAccountingAndExplicitContext(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		prompts = append(prompts, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"SUMMARY EVIDENCE\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	e := newTestEnv(t)
	id := seedAI(t, e, provider.URL)
	path := "/api/v1/ai/conversations/" + id
	r := e.do(t, "POST", path+"/summarize", map[string]any{"source": "logs", "logs": "secret log without consent"}, true)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("missing consent accepted")
	}
	mu.Lock()
	calls := len(prompts)
	mu.Unlock()
	if calls != 0 {
		t.Fatal("provider called without consent")
	}
	r = e.do(t, "POST", path+"/summarize", map[string]any{"source": "logs", "logs": strings.Repeat("event line\n", 200), "consent": true}, true)
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("summary %d %s", r.StatusCode, b)
	}
	result := decode[ai.LogSummaryResult](t, r)
	if result.Summary != "SUMMARY EVIDENCE" || len(result.Calls) < 2 {
		t.Fatalf("not map reduce: %+v", result)
	}
	messages, err := e.store.AIMessages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	recorded := 0
	for _, m := range messages {
		if m.Status == "summary_call" {
			recorded++
			if m.TokensIn == nil || *m.TokensIn != 10 || m.ServedModelID != "ai_model" {
				t.Fatal(m)
			}
		}
	}
	if recorded != len(result.Calls) {
		t.Fatalf("calls lost: %d/%d", recorded, len(result.Calls))
	}
	r = e.do(t, "POST", path+"/summarize", map[string]any{"source": "history", "consent": true}, true)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	r.Body.Close()
	summary := decode[store.AISummary](t, e.do(t, "GET", path+"/summary", nil, false))
	if summary.Content == "" || summary.ThroughMessage == 0 {
		t.Fatal(summary)
	}
	r = e.do(t, "PUT", path+"/facts", map[string]any{"content": "UNIQUE_CONFIRMED_FACT", "revision": 0}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	for _, include := range []bool{false, true} {
		r = e.do(t, "POST", path+"/messages", map[string]any{"content": "next question", "includeFacts": include, "includeSummary": include}, true)
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		mu.Lock()
		last := prompts[len(prompts)-1]
		mu.Unlock()
		var payload struct{ Messages []ai.Message }
		if err := json.Unmarshal([]byte(last), &payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(last, "UNIQUE_CONFIRMED_FACT") != include {
			t.Fatalf("facts opt-in ignored: %v", include)
		}
		if strings.Contains(last, "Previously generated, lossy conversation summary") != include {
			t.Fatalf("summary opt-in ignored: %v", include)
		}
	}
}
