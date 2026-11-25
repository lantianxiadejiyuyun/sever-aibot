package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/ai"
	"server-aibot/internal/store"
)

func TestAISummaryProviderFailurePreservesEvidence(t *testing.T) {
	for _, source := range []string{"logs", "history"} {
		t.Run(source, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 2 {
					http.Error(w, "PRIVATE_PROVIDER_FAILURE", http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"PRIVATE_PARTIAL_SUMMARY\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			e := newTestEnv(t)
			id := seedAI(t, e, provider.URL)
			path := "/api/v1/ai/conversations/" + id
			ctx := context.Background()
			logs := strings.Repeat("PRIVATE_RAW_LOG event\n", 40)
			var owner string
			if err := e.store.DB().QueryRow(`SELECT owner_id FROM ai_conversations WHERE id=?`, id).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if source == "history" {
				if _, err := e.store.AddAIMessage(ctx, id, store.AIMessage{Role: "user", Status: "completed", Content: logs}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.store.SaveAISummary(ctx, owner, id, store.AISummary{Content: "previous complete summary", ThroughMessage: 0}); err != nil {
				t.Fatal(err)
			}
			before := decode[store.AISummary](t, e.do(t, "GET", path+"/summary", nil, false))
			messagesBefore, err := e.store.AIMessages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			response := e.do(t, "POST", path+"/summarize", map[string]any{"source": source, "logs": logs, "consent": true}, true)
			if response.StatusCode != http.StatusBadGateway {
				response.Body.Close()
				t.Fatalf("failure status = %d", response.StatusCode)
			}
			var result struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
				Summary string              `json:"summary"`
				Calls   []ai.LogSummaryCall `json:"calls"`
			}
			result = decode[struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
				Summary string              `json:"summary"`
				Calls   []ai.LogSummaryCall `json:"calls"`
			}](t, response)
			if result.Error.Code != "summary_failed" || result.Summary != "" || calls.Load() != 2 || len(result.Calls) != 2 {
				t.Fatalf("unexpected failure result: %+v, provider calls=%d", result, calls.Load())
			}
			if !result.Calls[0].Succeeded || result.Calls[1].Succeeded {
				t.Fatalf("incorrect attempt outcomes: %+v", result.Calls)
			}
			messages, err := e.store.AIMessages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != len(messagesBefore)+2 {
				t.Fatalf("failure saved partial messages or lost calls: %+v", messages)
			}
			if !reflect.DeepEqual(messages[:len(messagesBefore)], messagesBefore) && len(messagesBefore) > 0 {
				t.Fatal("existing history changed")
			}
			for i, row := range messages[len(messagesBefore):] {
				assertSummaryFailureEvidence(t, row, result.Calls[i])
				for _, secret := range []string{"PRIVATE_RAW_LOG", "PRIVATE_PARTIAL_SUMMARY", "PRIVATE_PROVIDER_FAILURE"} {
					if strings.Contains(row.Content, secret) {
						t.Fatalf("call evidence leaked %s", secret)
					}
				}
			}
			if messages[len(messagesBefore)].TokensIn == nil || *messages[len(messagesBefore)].TokensIn != 10 || messages[len(messagesBefore)].TokensOut == nil || *messages[len(messagesBefore)].TokensOut != 2 {
				t.Fatal("successful attempt usage was lost")
			}
			failed := messages[len(messages)-1]
			if failed.TokensIn != nil || failed.TokensOut != nil || failed.Cost != nil {
				t.Fatalf("failed attempt invented usage: %+v", failed)
			}
			after := decode[store.AISummary](t, e.do(t, "GET", path+"/summary", nil, false))
			if after != before {
				t.Fatalf("failure replaced previous summary: before=%+v after=%+v", before, after)
			}
		})
	}
}

func assertSummaryFailureEvidence(t *testing.T, row store.AIMessage, want ai.LogSummaryCall) {
	t.Helper()
	if row.Role != "assistant" || row.Status != "summary_call" || row.ServedModelID != "ai_model" || row.EstimatedInput <= 0 || row.EstimatedInput != want.EstimatedInput || row.PriceSnapshot == "" {
		t.Fatalf("incomplete accounting: %+v", row)
	}
	var got ai.LogSummaryCall
	if err := json.Unmarshal([]byte(row.Content), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored evidence differs: got=%+v want=%+v", got, want)
	}
}

func TestAISummaryCancelledRequestPersistsAttemptAndReleasesSlot(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"PRIVATE_PARTIAL_SUMMARY\"}}]}\n\n")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"complete retry\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	e := newTestEnv(t)
	id := seedAI(t, e, provider.URL)
	path := "/api/v1/ai/conversations/" + id
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", e.srv.URL+path+"/summarize", strings.NewReader(`{"source":"logs","logs":"PRIVATE_RAW_LOG","consent":true}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	for _, cookie := range e.client.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); e.srv.Config.Handler.ServeHTTP(recorder, req) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	busy := e.do(t, "POST", path+"/summarize", map[string]any{"source": "logs", "logs": "duplicate", "consent": true}, true)
	busy.Body.Close()
	if busy.StatusCode != http.StatusConflict {
		t.Errorf("active summary did not reserve slot: %d", busy.StatusCode)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled summary did not finish")
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("cancelled handler status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Calls []ai.LogSummaryCall `json:"calls"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 || result.Calls[0].Succeeded || calls.Load() != 1 {
		t.Fatalf("cancelled attempt not recorded: %+v", result)
	}
	messages, err := e.store.AIMessages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("cancelled request lost evidence or saved partial summary: %+v", messages)
	}
	assertSummaryFailureEvidence(t, messages[0], result.Calls[0])
	if messages[0].TokensIn != nil || messages[0].TokensOut != nil || messages[0].Cost != nil || strings.Contains(messages[0].Content, "PRIVATE_") {
		t.Fatalf("unsafe cancelled accounting: %+v", messages[0])
	}
	summary := decode[store.AISummary](t, e.do(t, "GET", path+"/summary", nil, false))
	if summary.Content != "" || summary.ThroughMessage != 0 {
		t.Fatalf("partial summary saved: %+v", summary)
	}
	retry := e.do(t, "POST", path+"/summarize", map[string]any{"source": "logs", "logs": "retry", "consent": true}, true)
	if retry.StatusCode != http.StatusOK {
		retry.Body.Close()
		t.Fatalf("active slot not released: %d", retry.StatusCode)
	}
	completed := decode[ai.LogSummaryResult](t, retry)
	if completed.Summary != "complete retry" || calls.Load() != 2 {
		t.Fatalf("retry did not complete: %+v", completed)
	}
}

func TestAISummaryEndpointsEnforceOwnerIsolation(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "must not be called", 500) }))
	defer provider.Close()
	e := newTestEnv(t)
	ownID := seedAI(t, e, provider.URL)
	ctx := context.Background()
	owner := "other-summary-owner"
	_, err := e.store.DB().ExecContext(ctx, `INSERT INTO admins VALUES(?,?,?,?,?)`, owner, owner, "unused-test-hash", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	foreignID := "foreign-summary-conversation"
	if err := e.store.CreateAIConversation(ctx, owner, store.AIConversation{ID: foreignID, ModelID: "ai_model", Title: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AddAIMessage(ctx, foreignID, store.AIMessage{Role: "user", Status: "completed", Content: "FOREIGN_HISTORY"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveAISummary(ctx, owner, foreignID, store.AISummary{Content: "FOREIGN_SUMMARY"}); err != nil {
		t.Fatal(err)
	}
	before, err := e.store.AISummary(ctx, owner, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	messagesBefore, err := e.store.AIMessages(ctx, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{foreignID, "missing-summary-conversation"} {
		for _, method := range []string{"GET", "POST"} {
			suffix := "/summary"
			var body any
			if method == "POST" {
				suffix = "/summarize"
				body = map[string]any{"source": "history", "consent": true}
			}
			response := e.do(t, method, "/api/v1/ai/conversations/"+id+suffix, body, method == "POST")
			if response.StatusCode != http.StatusNotFound {
				response.Body.Close()
				t.Fatalf("%s %s returned %d", method, id, response.StatusCode)
			}
			payload := decode[map[string]any](t, response)
			raw, _ := json.Marshal(payload)
			if strings.Contains(string(raw), "FOREIGN_") {
				t.Fatalf("foreign summary exposed: %s", raw)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized summary called provider")
	}
	after, err := e.store.AISummary(ctx, owner, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	messagesAfter, err := e.store.AIMessages(ctx, foreignID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || !reflect.DeepEqual(messagesBefore, messagesAfter) {
		t.Fatal("unauthorized request changed foreign conversation")
	}
	own := e.do(t, "GET", "/api/v1/ai/conversations/"+ownID+"/summary", nil, false)
	own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Fatalf("owner cannot read own summary: %d", own.StatusCode)
	}
}
