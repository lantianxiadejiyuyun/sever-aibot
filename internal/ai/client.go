// Package ai implements only the OpenAI-compatible text Chat Completions API.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}
type ToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Result struct {
	Usage        *Usage
	FinishReason string
	ToolCalls    []ToolCall
}

const ProposalTool = `{"type":"function","function":{"name":"propose_forward_reapply","description":"Create an approval proposal to reapply an existing port forward. Never executes it. Human administrator confirmation is mandatory.","parameters":{"type":"object","properties":{"forwardId":{"type":"string"},"reason":{"type":"string"}},"required":["forwardId","reason"],"additionalProperties":false}}}`

type Client struct{ HTTP *http.Client }
type ProviderError struct{ Status int }

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return "provider connection failed"
	}
	return fmt.Sprintf("provider HTTP %d", e.Status)
}
func Retryable(err error) bool {
	var e *ProviderError
	return errors.As(err, &e) && (e.Status == 0 || e.Status == 408 || e.Status == 429 || e.Status >= 500)
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects are disabled") }}}
}
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("baseUrl must be an http(s) API base URL without credentials, query or fragment")
	}
	return nil
}

// Stream never returns provider response bodies in errors: those may echo secrets.
func (c *Client) Stream(ctx context.Context, base, key, model string, messages []Message, maxOutput int, emit func(string) error) (Result, error) {
	return c.stream(ctx, base, key, model, messages, maxOutput, false, emit)
}
func (c *Client) StreamWithProposals(ctx context.Context, base, key, model string, messages []Message, maxOutput int, emit func(string) error) (Result, error) {
	return c.stream(ctx, base, key, model, messages, maxOutput, true, emit)
}
func (c *Client) stream(ctx context.Context, base, key, model string, messages []Message, maxOutput int, tools bool, emit func(string) error) (Result, error) {
	var result Result
	if err := ValidateURL(base); err != nil {
		return result, err
	}
	payload := map[string]any{"model": model, "messages": messages, "stream": true, "max_tokens": maxOutput, "stream_options": map[string]bool{"include_usage": true}}
	if tools {
		payload["tools"] = ToolDefinitions()
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, &ProviderError{}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, &ProviderError{Status: res.StatusCode}
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return result, errors.New("provider did not return SSE")
	}
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	finished := false
	total := 0
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		if raw == "[DONE]" {
			finished = true
			return nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content   string          `json:"content"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			return errors.New("invalid provider SSE JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return errors.New("provider stream error")
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens < 0 || chunk.Usage.CompletionTokens < 0 {
				return errors.New("invalid provider usage")
			}
			result.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if len(choice.Delta.ToolCalls) > 0 && string(choice.Delta.ToolCalls) != "null" {
				if !tools {
					return errors.New("tools are not enabled")
				}
				var calls []ToolCall
				if json.Unmarshal(choice.Delta.ToolCalls, &calls) != nil {
					return errors.New("invalid tool delta")
				}
				for _, call := range calls {
					if call.Index < 0 || call.Index >= 3 {
						return errors.New("at most three proposals per turn")
					}
					for len(result.ToolCalls) <= call.Index {
						result.ToolCalls = append(result.ToolCalls, ToolCall{})
					}
					dst := &result.ToolCalls[call.Index]
					dst.ID += call.ID
					dst.Function.Name += call.Function.Name
					dst.Function.Arguments += call.Function.Arguments
					if len(dst.Function.Arguments) > 4096 || len(dst.Function.Name) > 80 || len(dst.ID) > 200 {
						return errors.New("tool arguments exceed limit")
					}
				}
			}
			if choice.FinishReason != nil {
				result.FinishReason = *choice.FinishReason
			}
			if choice.Delta.Content != "" {
				total += len(choice.Delta.Content)
				if total > 1<<20 {
					return errors.New("provider output exceeds 1 MiB limit")
				}
				if err := emit(choice.Delta.Content); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return result, err
			}
			if finished {
				return result, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if scanner.Err() != nil {
		return result, errors.New("provider stream read failed")
	}
	if err := dispatch(); err != nil {
		return result, err
	}
	if finished {
		return result, nil
	}
	return result, errors.New("provider stream ended without DONE")
}

type Budget struct {
	EstimatedInput  int    `json:"estimatedInput"`
	OutputReserve   int    `json:"outputReserve"`
	ContextWindow   int    `json:"contextWindow"`
	DroppedMessages int    `json:"droppedMessages"`
	Method          string `json:"method"`
	SummaryMethod   string `json:"summaryMethod,omitempty"`
	SummaryBytes    int    `json:"summaryBytes,omitempty"`
}

// BudgetMessages uses UTF-8 bytes + 16 framing tokens/message, deliberately
// conservative for byte-based tokenizers. It is an estimate, not tokenization.
// Preserve system + newest user turn; remove whole older user/assistant pairs.
func BudgetMessages(system string, history []Message, current string, window, reserve int) ([]Message, Budget, error) {
	b := Budget{OutputReserve: reserve, ContextWindow: window, Method: "conservative_utf8_bytes_plus_16_per_message"}
	messages := []Message{{Role: "system", Content: system}}
	messages = append(messages, history...)
	messages = append(messages, Message{Role: "user", Content: current})
	cost := func() int {
		n := 16
		for _, m := range messages {
			n += len(m.Content) + 16
		}
		return n
	}
	for cost()+reserve > window && len(messages) > 2 {
		end := 2
		for end < len(messages)-1 && messages[end].Role != "user" {
			end++
		}
		b.DroppedMessages += end - 1
		messages = append(messages[:1], messages[end:]...)
	}
	b.EstimatedInput = cost()
	if b.EstimatedInput+reserve > window {
		return nil, b, errors.New("system/context and latest message exceed model context budget")
	}
	return messages, b, nil
}
