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

// anthropicStream sends one request to the Anthropic Messages API and
// turns its server-sent events into Events on the channel.
func anthropicStream(ctx context.Context, m Model, c Context, opts Options, out chan<- Event) error {
	body := buildAnthropicRequest(m, c, opts)
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.BaseURL, "/")+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", opts.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
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
	// Partial JSON for tool call arguments, by content index.
	argBuf := map[int]*strings.Builder{}
	var streamErr error

	err = readSSE(resp.Body, func(ev sseEvent) bool {
		var raw struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				Data     string          `json:"data"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(ev.Data), &raw) != nil {
			return true
		}
		switch raw.Type {
		case "message_start":
			msg.Usage.Input = raw.Message.Usage.InputTokens
			msg.Usage.CacheRead = raw.Message.Usage.CacheReadInputTokens
			msg.Usage.CacheWrite = raw.Message.Usage.CacheCreationInputTokens
		case "content_block_start":
			idx := len(msg.Content)
			switch raw.ContentBlock.Type {
			case "text":
				msg.Content = append(msg.Content, Content{Type: "text", Text: raw.ContentBlock.Text})
				out <- Event{Type: "text_start", Index: idx, Partial: msg}
			case "thinking":
				msg.Content = append(msg.Content, Content{Type: "thinking", Thinking: raw.ContentBlock.Thinking})
				out <- Event{Type: "thinking_start", Index: idx, Partial: msg}
			case "redacted_thinking":
				msg.Content = append(msg.Content, Content{Type: "thinking", Redacted: true, Signature: raw.ContentBlock.Data})
				out <- Event{Type: "thinking_start", Index: idx, Partial: msg}
			case "tool_use":
				msg.Content = append(msg.Content, Content{Type: "toolCall", ID: raw.ContentBlock.ID, Name: raw.ContentBlock.Name})
				argBuf[idx] = &strings.Builder{}
				out <- Event{Type: "toolcall_start", Index: idx, Partial: msg}
			default:
				msg.Content = append(msg.Content, Content{Type: "text"})
			}
		case "content_block_delta":
			idx := raw.Index
			if idx < 0 || idx >= len(msg.Content) {
				return true
			}
			blk := &msg.Content[idx]
			switch raw.Delta.Type {
			case "text_delta":
				blk.Text += raw.Delta.Text
				out <- Event{Type: "text_delta", Index: idx, Delta: raw.Delta.Text, Partial: msg}
			case "thinking_delta":
				blk.Thinking += raw.Delta.Thinking
				out <- Event{Type: "thinking_delta", Index: idx, Delta: raw.Delta.Thinking, Partial: msg}
			case "signature_delta":
				blk.Signature += raw.Delta.Signature
			case "input_json_delta":
				if b := argBuf[idx]; b != nil {
					b.WriteString(raw.Delta.PartialJSON)
				}
				out <- Event{Type: "toolcall_delta", Index: idx, Delta: raw.Delta.PartialJSON, Partial: msg}
			}
		case "content_block_stop":
			idx := raw.Index
			if idx < 0 || idx >= len(msg.Content) {
				return true
			}
			blk := &msg.Content[idx]
			switch blk.Type {
			case "text":
				out <- Event{Type: "text_end", Index: idx, Content: blk.Text, Partial: msg}
			case "thinking":
				out <- Event{Type: "thinking_end", Index: idx, Content: blk.Thinking, Partial: msg}
			case "toolCall":
				args := "{}"
				if b := argBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				if !json.Valid([]byte(args)) {
					args = "{}"
				}
				blk.Arguments = json.RawMessage(args)
				tc := *blk
				out <- Event{Type: "toolcall_end", Index: idx, ToolCall: &tc, Partial: msg}
			}
		case "message_delta":
			msg.Usage.Output = raw.Usage.OutputTokens
			switch raw.Delta.StopReason {
			case "end_turn", "stop_sequence", "pause_turn":
				msg.StopReason = StopStop
			case "max_tokens":
				msg.StopReason = StopLength
			case "tool_use":
				msg.StopReason = StopToolUse
			case "refusal":
				msg.StopReason = StopError
				msg.ErrorMessage = "the model declined this request (stop_reason: refusal)"
			}
		case "error":
			streamErr = fmt.Errorf("%s: %s", raw.Error.Type, raw.Error.Message)
			return false
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
	if msg.StopReason == StopPending {
		msg.StopReason = StopStop
	}
	if msg.StopReason == StopError {
		out <- Event{Type: "error", Reason: StopError, Message: msg}
		return nil
	}
	if len(msg.ToolCalls()) > 0 && msg.StopReason == StopStop {
		msg.StopReason = StopToolUse
	}
	out <- Event{Type: "done", Reason: msg.StopReason, Message: msg}
	return nil
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// buildAnthropicRequest converts our Context into the Messages API body.
func buildAnthropicRequest(m Model, c Context, opts Options) map[string]any {
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = m.MaxTokens
	}
	body := map[string]any{
		"model":      m.ID,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   anthropicMessages(c.Messages, m),
	}
	if c.SystemPrompt != "" {
		body["system"] = []map[string]any{{
			"type": "text", "text": c.SystemPrompt,
			"cache_control": map[string]string{"type": "ephemeral"},
		}}
	}
	if len(c.Tools) > 0 {
		tools := make([]map[string]any, 0, len(c.Tools))
		for _, t := range c.Tools {
			tools = append(tools, map[string]any{
				"name": t.Name, "description": t.Description, "input_schema": t.Parameters,
			})
		}
		body["tools"] = tools
	}
	if m.Reasoning && opts.ThinkingLevel != "" && opts.ThinkingLevel != "off" {
		if usesBudgetTokens(m.ID) {
			budget := map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 24000, "max": 32000}[opts.ThinkingLevel]
			if budget >= maxTokens {
				budget = maxTokens / 2
			}
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		} else {
			effort := opts.ThinkingLevel
			if effort == "minimal" {
				effort = "low"
			}
			body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
			body["output_config"] = map[string]any{"effort": effort}
		}
	}
	return body
}

// usesBudgetTokens is true for models older than the 4.6 generation.
func usesBudgetTokens(id string) bool {
	for _, old := range []string{"haiku-4-5", "sonnet-4-5", "opus-4-5", "opus-4-1", "-3-", "sonnet-4-2", "opus-4-2"} {
		if strings.Contains(id, old) {
			return true
		}
	}
	return false
}

func anthropicMessages(msgs []Message, m Model) []map[string]any {
	var out []map[string]any
	// Tool results must sit together in one user message right after the call.
	var pendingResults []map[string]any
	flushResults := func() {
		if len(pendingResults) > 0 {
			out = append(out, map[string]any{"role": "user", "content": pendingResults})
			pendingResults = nil
		}
	}
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			flushResults()
			var blocks []map[string]any
			for _, c := range msg.Content {
				switch c.Type {
				case "text":
					if c.Text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
					}
				case "image":
					if m.SupportsImages() {
						blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
							"type": "base64", "media_type": c.MimeType, "data": c.Data}})
					}
				}
			}
			if len(blocks) == 0 {
				blocks = []map[string]any{{"type": "text", "text": "(empty)"}}
			}
			out = append(out, map[string]any{"role": "user", "content": blocks})
		case "assistant":
			flushResults()
			var blocks []map[string]any
			for _, c := range msg.Content {
				switch c.Type {
				case "text":
					if strings.TrimSpace(c.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
					}
				case "thinking":
					// Only replay thinking to the same model; others reject or ignore it.
					if msg.Model != m.ID || c.Signature == "" {
						continue
					}
					if c.Redacted {
						blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": c.Signature})
					} else {
						blocks = append(blocks, map[string]any{"type": "thinking", "thinking": c.Thinking, "signature": c.Signature})
					}
				case "toolCall":
					args := c.Arguments
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": args})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case "toolResult":
			var content []map[string]any
			for _, c := range msg.Content {
				switch c.Type {
				case "text":
					content = append(content, map[string]any{"type": "text", "text": c.Text})
				case "image":
					if m.SupportsImages() {
						content = append(content, map[string]any{"type": "image", "source": map[string]any{
							"type": "base64", "media_type": c.MimeType, "data": c.Data}})
					}
				}
			}
			if len(content) == 0 {
				content = []map[string]any{{"type": "text", "text": "(no output)"}}
			}
			pendingResults = append(pendingResults, map[string]any{
				"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": content, "is_error": msg.IsError,
			})
		}
	}
	flushResults()
	return out
}
