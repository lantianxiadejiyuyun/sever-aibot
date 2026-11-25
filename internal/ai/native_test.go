package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeAnthropicTextAndTool(t *testing.T) {
	for _, tool := range []bool{false, true} {
		t.Run(fmt.Sprint(tool), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("wrong native request %s", r.URL.Path)
				}
				var request map[string]any
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request["system"] == nil {
					t.Error("system missing")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"fake\",\"usage\":{\"input_tokens\":8,\"output_tokens\":0}}}\n\n")
				stop := "end_turn"
				if tool {
					stop = "tool_use"
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"propose_forward_reapply\",\"input\":{}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"forwardId\\\":\\\"f1\\\",\\\"reason\\\":\\\"retry\\\"}\"}}\n\n")
				} else {
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"native\"}}\n\n")
				}
				fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", stop)
			}))
			defer srv.Close()
			var out strings.Builder
			result, err := NewClient().Generate(context.Background(), "anthropic", srv.URL, "test-key", "fake", []Message{{Role: "system", Content: "system"}, {Role: "user", Content: "hi"}}, 100, tool, func(text string) error { out.WriteString(text); return nil })
			if err != nil || result.Usage == nil || result.Usage.PromptTokens != 8 || result.Usage.CompletionTokens != 3 {
				t.Fatalf("%+v %v", result, err)
			}
			if tool {
				if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "propose_forward_reapply" || result.FinishReason != "tool_calls" {
					t.Fatalf("tools %+v", result)
				}
			} else if out.String() != "native" {
				t.Fatalf("text %q", out.String())
			}
		})
	}
}
func TestNativeGeminiTextAndTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/fake:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("wrong Gemini request")
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["systemInstruction"] == nil || request["tools"] == nil {
			t.Error("missing native fields")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"native"}]}}]}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"propose_forward_reapply","args":{"forwardId":"f1","reason":"retry"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"thoughtsTokenCount":2}}

`)
	}))
	defer srv.Close()
	var out strings.Builder
	result, err := NewClient().Generate(context.Background(), "gemini", srv.URL+"/v1beta", "test-key", "fake", []Message{{Role: "system", Content: "system"}, {Role: "user", Content: "hi"}}, 100, true, func(text string) error { out.WriteString(text); return nil })
	if err != nil || out.String() != "native" || len(result.ToolCalls) != 1 || result.FinishReason != "tool_calls" || result.Usage == nil || result.Usage.CompletionTokens != 5 {
		t.Fatalf("%+v %v", result, err)
	}
}
