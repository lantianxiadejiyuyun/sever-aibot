package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic uses the native Messages SDK, never the OpenAI compatibility API.
func (c *Client) anthropicStream(ctx context.Context, base, key, model string, messages []Message, maxOutput int, tools bool, emit func(string) error) (Result, error) {
	var result Result
	if err := ValidateURL(base); err != nil {
		return result, err
	}
	if key == "" {
		return result, errors.New("Anthropic requires a bound API credential")
	}
	// The explicit SDK auth source prevents environment/profile credentials from
	// being selected instead of the administrator's vault binding.
	sdk := anthropic.NewClient(option.WithAPIKey(key), option.WithBaseURL(strings.TrimRight(base, "/")+"/"), option.WithHTTPClient(c.HTTP), option.WithMaxRetries(0))
	params := anthropic.MessageNewParams{Model: anthropic.Model(model), MaxTokens: int64(maxOutput)}
	for _, m := range messages {
		switch m.Role {
		case "system":
			params.System = append(params.System, anthropic.TextBlockParam{Text: m.Content})
		case "user":
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	if tools {
		for _, definition := range ToolDefinitions() {
			var source struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			_ = json.Unmarshal(definition, &source)
			raw, _ := json.Marshal(map[string]any{"name": source.Function.Name, "description": source.Function.Description, "input_schema": source.Function.Parameters})
			var tool anthropic.ToolUnionParam
			if err := json.Unmarshal(raw, &tool); err != nil {
				return result, errors.New("invalid tool schema")
			}
			params.Tools = append(params.Tools, tool)
		}
	}
	stream := sdk.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	message := anthropic.Message{}
	stopped := false
	total := 0
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return result, errors.New("invalid Anthropic stream")
		}
		switch v := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if d, ok := v.Delta.AsAny().(anthropic.TextDelta); ok {
				total += len(d.Text)
				if total > 1<<20 {
					return result, errors.New("provider output exceeds 1 MiB limit")
				}
				if err := emit(d.Text); err != nil {
					return result, err
				}
			}
		case anthropic.MessageStopEvent:
			stopped = true
		}
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return result, &ProviderError{Status: apiErr.StatusCode}
		}
		return result, errors.New("Anthropic stream failed")
	}
	if !stopped {
		return result, errors.New("Anthropic stream ended without message_stop")
	}
	result.Usage = &Usage{PromptTokens: int(message.Usage.InputTokens + message.Usage.CacheReadInputTokens + message.Usage.CacheCreationInputTokens), CompletionTokens: int(message.Usage.OutputTokens)}
	switch string(message.StopReason) {
	case "end_turn", "stop_sequence":
		result.FinishReason = "stop"
	case "max_tokens":
		result.FinishReason = "length"
	case "tool_use":
		result.FinishReason = "tool_calls"
	case "refusal":
		return result, errors.New("provider refused this request")
	default:
		return result, errors.New("unsupported Anthropic stop reason")
	}
	for _, block := range message.Content {
		if v, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			if !tools || len(result.ToolCalls) >= 3 || len(v.Input) > 4096 {
				return result, errors.New("invalid Anthropic tool request")
			}
			call := ToolCall{ID: v.ID}
			call.Function.Name = v.Name
			call.Function.Arguments = string(v.Input)
			result.ToolCalls = append(result.ToolCalls, call)
		}
	}
	return result, nil
}

// Generate selects an actual provider-specific protocol adapter.
func (c *Client) Generate(ctx context.Context, kind, base, key, model string, messages []Message, maxOutput int, tools bool, emit func(string) error) (Result, error) {
	switch kind {
	case "openai_compatible":
		return c.stream(ctx, base, key, model, messages, maxOutput, tools, emit)
	case "anthropic":
		return c.anthropicStream(ctx, base, key, model, messages, maxOutput, tools, emit)
	case "gemini":
		return c.geminiStream(ctx, base, key, model, messages, maxOutput, tools, emit)
	default:
		return Result{}, errors.New("unsupported provider kind")
	}
}
