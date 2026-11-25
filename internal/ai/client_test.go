package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamDeliversBeforeCompletionAndUsage(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test" {
			t.Error("wrong request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	var output string
	result, err := NewClient().Stream(context.Background(), srv.URL+"/v1", "local-test", "fake", []Message{{Role: "user", Content: "hello"}}, 8, func(token string) error {
		if output == "" {
			close(release)
		}
		output += token
		return nil
	})
	if err != nil || output != "你好" || result.Usage == nil || result.Usage.PromptTokens != 10 {
		t.Fatalf("stream=%+v %q %v", result, output, err)
	}
}
func TestStreamCancelAndUnexpectedEOF(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := NewClient().Stream(ctx, srv.URL, "", "fake", nil, 8, func(string) error { cancel(); return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
	for _, body := range []string{"data: {bad}\n\n", "data: {\"choices\":[]}\n\n", "data: {\"error\":{\"message\":\"secret\"}}\n\n"} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			_, err := NewClient().Stream(context.Background(), srv.URL, "", "fake", nil, 8, func(string) error { return nil })
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}
func TestBudgetPreservesLatestAndWholeTurns(t *testing.T) {
	history := []Message{{Role: "user", Content: strings.Repeat("a", 60)}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "recent"}, {Role: "assistant", Content: "recent answer"}}
	messages, budget, err := BudgetMessages("system", history, "latest", 160, 20)
	if err != nil || budget.DroppedMessages != 2 || len(messages) != 4 || messages[1].Content != "recent" || messages[3].Content != "latest" {
		t.Fatalf("%+v %+v %v", messages, budget, err)
	}
	if _, _, err := BudgetMessages("system", nil, strings.Repeat("你", 100), 100, 10); err == nil {
		t.Fatal("oversize latest must reject")
	}
}
func TestToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call1","function":{"name":"propose_forward_reapply","arguments":"{\"forwardId\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"f1\",\"reason\":\"retry\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	}))
	defer srv.Close()
	result, err := NewClient().StreamWithProposals(context.Background(), srv.URL, "", "fake", nil, 30, func(string) error { return nil })
	if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Arguments != `{"forwardId":"f1","reason":"retry"}` {
		t.Fatalf("%+v %v", result, err)
	}
}
