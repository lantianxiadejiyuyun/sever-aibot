package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) geminiStream(ctx context.Context, base, key, model string, messages []Message, maxOutput int, tools bool, emit func(string) error) (Result, error) {
	var result Result
	if err := ValidateURL(base); err != nil {
		return result, err
	}
	if key == "" {
		return result, errors.New("Gemini requires a bound API credential")
	}
	contents := []map[string]any{}
	system := []map[string]string{}
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, map[string]string{"text": m.Content})
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]any{"role": role, "parts": []map[string]string{{"text": m.Content}}})
	}
	payload := map[string]any{"contents": contents, "systemInstruction": map[string]any{"parts": system}, "generationConfig": map[string]any{"maxOutputTokens": maxOutput, "candidateCount": 1}}
	if tools {
		functions := []any{}
		for _, definition := range ToolDefinitions() {
			var source struct {
				Function map[string]any `json:"function"`
			}
			_ = json.Unmarshal(definition, &source)
			source.Function["parametersJsonSchema"] = source.Function["parameters"]
			delete(source.Function, "parameters")
			functions = append(functions, source.Function)
		}
		payload["tools"] = []map[string]any{{"functionDeclarations": functions}}
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(base, "/") + "/models/" + url.PathEscape(strings.TrimPrefix(model, "models/")) + ":streamGenerateContent?alt=sse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-goog-api-key", key)
	response, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, &ProviderError{}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, &ProviderError{Status: response.StatusCode}
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return result, errors.New("Gemini did not return SSE")
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	total := 0
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		var chunk struct {
			Error          json.RawMessage `json:"error"`
			PromptFeedback struct {
				BlockReason string `json:"blockReason"`
			} `json:"promptFeedback"`
			Usage *struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
				Thoughts   int `json:"thoughtsTokenCount"`
			} `json:"usageMetadata"`
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text         string `json:"text"`
						Thought      bool   `json:"thought"`
						FunctionCall *struct {
							Name string          `json:"name"`
							Args json.RawMessage `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
				Finish string `json:"finishReason"`
			} `json:"candidates"`
		}
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			return errors.New("invalid Gemini SSE JSON")
		}
		if len(chunk.Error) > 0 || chunk.PromptFeedback.BlockReason != "" {
			return errors.New("Gemini rejected the request")
		}
		if chunk.Usage != nil {
			if chunk.Usage.Prompt < 0 || chunk.Usage.Candidates < 0 || chunk.Usage.Thoughts < 0 {
				return errors.New("invalid Gemini usage")
			}
			result.Usage = &Usage{PromptTokens: chunk.Usage.Prompt, CompletionTokens: chunk.Usage.Candidates + chunk.Usage.Thoughts}
		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if !part.Thought && part.Text != "" {
					total += len(part.Text)
					if total > 1<<20 {
						return errors.New("provider output exceeds 1 MiB limit")
					}
					if err := emit(part.Text); err != nil {
						return err
					}
				}
				if call := part.FunctionCall; call != nil {
					if !tools || len(result.ToolCalls) >= 3 || len(call.Args) > 4096 {
						return errors.New("invalid Gemini tool request")
					}
					tool := ToolCall{}
					tool.Function.Name = call.Name
					tool.Function.Arguments = string(call.Args)
					result.ToolCalls = append(result.ToolCalls, tool)
				}
			}
			switch candidate.Finish {
			case "":
			case "STOP":
				result.FinishReason = "stop"
			case "MAX_TOKENS":
				result.FinishReason = "length"
			default:
				return errors.New("Gemini stopped for safety or invalid output")
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
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if scanner.Err() != nil {
		return result, errors.New("Gemini stream read failed")
	}
	if err := dispatch(); err != nil {
		return result, err
	}
	if result.FinishReason == "" {
		return result, errors.New("Gemini stream ended without finishReason")
	}
	if len(result.ToolCalls) > 0 && result.FinishReason == "stop" {
		result.FinishReason = "tool_calls"
	}
	return result, nil
}
