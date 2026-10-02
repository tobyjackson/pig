package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openaiStream sends one request to an OpenAI-compatible Chat Completions API.
func openaiStream(ctx context.Context, m Model, c Context, opts Options, out chan<- Event) error {
	body := buildOpenAIRequest(m, c, opts)
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if opts.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+opts.APIKey)
	}
	for k, v := range m.Headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &HTTPError{Status: resp.StatusCode, Body: string(b)}
	}

	msg := &Message{Role: "assistant", Content: []Content{}, Provider: m.Provider, Model: m.ID, Usage: &Usage{}, StopReason: StopPending, Timestamp: Now()}
	out <- Event{Type: "start", Partial: msg}
	textIdx, thinkIdx := -1, -1
	toolIdx := map[int]int{} // tool_calls[i] -> content index
	argBuf := map[int]*strings.Builder{}
	var streamErr error

	err = readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.Data == "[DONE]" {
			return false
		}
		var raw struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				Cost             float64 `json:"cost"` // OpenRouter reports dollars spent
				PromptDetails    struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ev.Data), &raw) != nil {
			return true
		}
		if raw.Error != nil {
			streamErr = fmt.Errorf("%s", raw.Error.Message)
			return false
		}
		if raw.Usage != nil {
			msg.Usage.Input = raw.Usage.PromptTokens - raw.Usage.PromptDetails.CachedTokens
			msg.Usage.CacheRead = raw.Usage.PromptDetails.CachedTokens
			msg.Usage.Output = raw.Usage.CompletionTokens
			msg.Usage.Cost = raw.Usage.Cost
		}
		for _, ch := range raw.Choices {
			d := ch.Delta
			if r := d.Reasoning + d.ReasoningContent; r != "" {
				if thinkIdx < 0 {
					thinkIdx = len(msg.Content)
					msg.Content = append(msg.Content, Content{Type: "thinking"})
					out <- Event{Type: "thinking_start", Index: thinkIdx, Partial: msg}
				}
				msg.Content[thinkIdx].Thinking += r
				out <- Event{Type: "thinking_delta", Index: thinkIdx, Delta: r, Partial: msg}
			}
			if d.Content != "" {
				if thinkIdx >= 0 && textIdx < 0 {
					out <- Event{Type: "thinking_end", Index: thinkIdx, Content: msg.Content[thinkIdx].Thinking, Partial: msg}
				}
				if textIdx < 0 {
					textIdx = len(msg.Content)
					msg.Content = append(msg.Content, Content{Type: "text"})
					out <- Event{Type: "text_start", Index: textIdx, Partial: msg}
				}
				msg.Content[textIdx].Text += d.Content
				out <- Event{Type: "text_delta", Index: textIdx, Delta: d.Content, Partial: msg}
			}
			for _, tc := range d.ToolCalls {
				idx, ok := toolIdx[tc.Index]
				if !ok {
					idx = len(msg.Content)
					toolIdx[tc.Index] = idx
					msg.Content = append(msg.Content, Content{Type: "toolCall", ID: tc.ID, Name: tc.Function.Name})
					argBuf[idx] = &strings.Builder{}
					out <- Event{Type: "toolcall_start", Index: idx, Partial: msg}
				}
				if tc.ID != "" {
					msg.Content[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					msg.Content[idx].Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					argBuf[idx].WriteString(tc.Function.Arguments)
					out <- Event{Type: "toolcall_delta", Index: idx, Delta: tc.Function.Arguments, Partial: msg}
				}
			}
			switch ch.FinishReason {
			case "stop":
				msg.StopReason = StopStop
			case "length":
				msg.StopReason = StopLength
			case "tool_calls", "function_call":
				msg.StopReason = StopToolUse
			case "content_filter":
				msg.StopReason = StopError
				msg.ErrorMessage = "the model declined this request (content_filter)"
			}
		}
		return true
	})
	if err != nil && streamErr == nil {
		streamErr = err
	}
	if ctx.Err() != nil {
		streamErr = ctx.Err()
	}
	ComputeCost(m, msg.Usage)
	if streamErr != nil {
		return streamErr
	}
	if textIdx >= 0 {
		out <- Event{Type: "text_end", Index: textIdx, Content: msg.Content[textIdx].Text, Partial: msg}
	}
	for _, idx := range toolIdx {
		args := "{}"
		if b := argBuf[idx]; b != nil && b.Len() > 0 && json.Valid([]byte(b.String())) {
			args = b.String()
		}
		msg.Content[idx].Arguments = json.RawMessage(args)
		if msg.Content[idx].ID == "" {
			msg.Content[idx].ID = fmt.Sprintf("call_%d", idx)
		}
		tc := msg.Content[idx]
		out <- Event{Type: "toolcall_end", Index: idx, ToolCall: &tc, Partial: msg}
	}
	if msg.StopReason == StopPending {
		msg.StopReason = StopStop
	}
	if len(toolIdx) > 0 && msg.StopReason == StopStop {
		msg.StopReason = StopToolUse
	}
	if msg.StopReason == StopError {
		out <- Event{Type: "error", Reason: StopError, Message: msg}
		return nil
	}
	out <- Event{Type: "done", Reason: msg.StopReason, Message: msg}
	return nil
}

func buildOpenAIRequest(m Model, c Context, opts Options) map[string]any {
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = m.MaxTokens
	}
	body := map[string]any{
		"model":                 m.ID,
		"stream":                true,
		"stream_options":        map[string]any{"include_usage": true},
		"max_completion_tokens": maxTokens,
		"messages":              openaiMessages(c, m),
	}
	if m.Provider == "openrouter" {
		// Ask OpenRouter to include the exact cost with the usage block.
		body["usage"] = map[string]any{"include": true}
	}
	if len(c.Tools) > 0 {
		tools := make([]map[string]any, 0, len(c.Tools))
		for _, t := range c.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}})
		}
		body["tools"] = tools
	}
	if m.Reasoning {
		switch {
		case opts.ThinkingLevel == "" || opts.ThinkingLevel == "off":
			// OpenRouter normalises reasoning through a nested object and falls
			// back to the provider's own default when it is absent, so "off" has
			// to be sent as effort "none" or the model thinks anyway.
			if m.Provider == "openrouter" {
				body["reasoning"] = map[string]any{"effort": "none"}
			}
		case m.Provider == "openrouter":
			body["reasoning"] = map[string]any{"effort": opts.ThinkingLevel}
		default:
			effort := opts.ThinkingLevel
			if effort == "xhigh" || effort == "max" {
				effort = "high"
			}
			body["reasoning_effort"] = effort
		}
	}
	return body
}

func openaiMessages(c Context, m Model) []map[string]any {
	var out []map[string]any
	if c.SystemPrompt != "" {
		out = append(out, map[string]any{"role": "system", "content": c.SystemPrompt})
	}
	for _, msg := range c.Messages {
		switch msg.Role {
		case "user":
			var parts []map[string]any
			for _, blk := range msg.Content {
				switch blk.Type {
				case "text":
					parts = append(parts, map[string]any{"type": "text", "text": blk.Text})
				case "image":
					if m.SupportsImages() {
						parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{
							"url": "data:" + blk.MimeType + ";base64," + blk.Data}})
					}
				}
			}
			out = append(out, map[string]any{"role": "user", "content": parts})
		case "assistant":
			entry := map[string]any{"role": "assistant"}
			text := msg.TextContent()
			if text != "" {
				entry["content"] = text
			}
			var calls []map[string]any
			for _, tc := range msg.ToolCalls() {
				args := string(tc.Arguments)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{
					"name": tc.Name, "arguments": args}})
			}
			if len(calls) > 0 {
				entry["tool_calls"] = calls
			}
			if text == "" && len(calls) == 0 {
				continue
			}
			out = append(out, entry)
		case "toolResult":
			var sb strings.Builder
			for _, blk := range msg.Content {
				if blk.Type == "text" {
					sb.WriteString(blk.Text)
				} else if blk.Type == "image" {
					sb.WriteString("[image omitted]")
				}
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": msg.ToolCallID, "content": sb.String()})
		}
	}
	return out
}
