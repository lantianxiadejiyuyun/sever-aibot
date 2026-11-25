package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server-aibot/internal/store"
)

func seedAI(t *testing.T, env *testEnv, base string) string {
	t.Helper()
	ctx := context.Background()
	p := store.AIProvider{ID: "ai_provider", Name: "fake", Kind: "openai_compatible", BaseURL: base, Enabled: true}
	m := store.AIModel{ID: "ai_model", ProviderID: p.ID, Name: "fake", ModelID: "fake", ContextWindow: 4096, MaxOutput: 100, Enabled: true}
	if err := env.store.SaveAIConfig(ctx, "providers", p.ID, "", "", p, true); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveAIConfig(ctx, "models", m.ID, "", p.ID, m, true); err != nil {
		t.Fatal(err)
	}
	response := env.do(t, "POST", "/api/v1/ai/conversations", map[string]any{"modelId": m.ID, "title": "test"}, true)
	if response.StatusCode != 201 {
		t.Fatalf("create: %d", response.StatusCode)
	}
	v := decode[store.AIConversation](t, response)
	return v.ID
}
func TestAIStreamingPersistenceAndCancel(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if input.Messages[len(input.Messages)-1].Content == "cancel" {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" final\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	env := newTestEnv(t)
	id := seedAI(t, env, provider.URL)
	response := env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/messages", map[string]string{"content": "hello"}, true)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "event: token") || !strings.Contains(string(body), "event: done") {
		t.Fatalf("SSE %s", body)
	}
	stored, err := env.store.AIMessages(context.Background(), id)
	if err != nil || len(stored) != 2 || stored[1].Content != "first final" || stored[1].TokensIn == nil || *stored[1].TokensIn != 12 {
		t.Fatalf("stored %+v %v", stored, err)
	}
	response = env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/messages", map[string]string{"content": "cancel"}, true)
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: token" {
			break
		}
	}
	duplicate := env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/messages", map[string]string{"content": "duplicate"}, true)
	duplicate.Body.Close()
	if duplicate.StatusCode != 409 {
		t.Fatalf("concurrent request: %d", duplicate.StatusCode)
	}
	cancelled := env.do(t, "POST", "/api/v1/ai/conversations/"+id+"/cancel", map[string]any{}, true)
	cancelled.Body.Close()
	for scanner.Scan() {
	}
	response.Body.Close()
	stored, err = env.store.AIMessages(context.Background(), id)
	if err != nil || len(stored) != 4 || stored[3].Status != "cancelled" || stored[3].Content != "first" {
		t.Fatalf("cancelled %+v %v", stored, err)
	}
}
func TestAIApprovalRequiresCSRFStepUpConfirmationAndCannotReplay(t *testing.T) {
	env := newTestEnv(t)
	id := seedAI(t, env, "http://127.0.0.1:1/v1")
	p := store.AIProposal{ID: "proposal", ConversationID: id, Tool: "propose_forward_reapply", Target: "missing", Status: "proposed"}
	if err := env.store.CreateAIProposal(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ai/tool-calls/proposal/approve"
	for _, csrf := range []bool{false, true} {
		res := env.do(t, "POST", path, map[string]bool{"confirm": true}, csrf)
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatalf("without csrf/stepup: %d", res.StatusCode)
		}
	}
	_, err := env.store.DB().Exec(`UPDATE admin_sessions SET step_up_until=?`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	res := env.do(t, "POST", path, map[string]bool{"confirm": false}, true)
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("confirmation: %d", res.StatusCode)
	}
	res = env.do(t, "POST", path, map[string]bool{"confirm": true}, true)
	if res.StatusCode != 200 {
		t.Fatalf("approve: %d", res.StatusCode)
	}
	result := decode[store.AIProposal](t, res)
	if result.Status != "failed" {
		t.Fatalf("invalid target executed: %+v", result)
	}
	res = env.do(t, "POST", path, map[string]bool{"confirm": true}, true)
	res.Body.Close()
	if res.StatusCode != 409 {
		t.Fatalf("replay: %d", res.StatusCode)
	}
}
func TestAIProviderConfigNeverReturnsCredentialSecret(t *testing.T) {
	env := newTestEnv(t)
	seedAI(t, env, "http://127.0.0.1:1/v1")
	res := env.do(t, "PATCH", "/api/v1/ai/providers/ai_provider", map[string]string{"kind": "anthropic"}, true)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("configuration must require stepup: %d", res.StatusCode)
	}
	_, err := env.store.DB().Exec(`UPDATE admin_sessions SET step_up_until=?`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	res = env.do(t, "PATCH", "/api/v1/ai/providers/ai_provider", map[string]string{"kind": "unsupported_native_provider"}, true)
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("fake compatibility accepted: %d", res.StatusCode)
	}
	res = env.do(t, "GET", "/api/v1/ai/providers", nil, false)
	payload := decode[map[string]any](t, res)
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "apiKey") {
		t.Fatalf("unsafe config: %s", encoded)
	}
}
