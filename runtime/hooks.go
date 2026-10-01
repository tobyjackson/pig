package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/compaction"
	"github.com/tobyjackson/pig/tools"
)

// installHooks wires the agent's callbacks to this session: events out,
// messages into the session file, compaction before a turn, and the
// extension hooks for input, tool calls and tool results.
func (s *Session) installHooks() {
	a := s.Agent
	a.Subscribe(func(e Event) { s.emit(e) })
	// A queued message is filtered when the agent consumes it, not when it is
	// queued, so an extension can still see steers and follow-ups without
	// blocking the caller that sent them.
	a.Hooks.FilterQueued = func(m ai.Message) (ai.Message, bool) {
		if ext := s.applyInputHooks(m, true); ext.Role != "" {
			return ext, true
		}
		return m, false
	}
	a.Hooks.OnMessage = func(m ai.Message) { s.Store.AppendMessage(m) }
	a.Hooks.BeforeTurn = func(ctx context.Context) error {
		if !s.autoCompaction {
			return nil
		}
		msgs := a.Messages()
		if compaction.ShouldCompact(compaction.ContextTokens(a.SystemPrompt, msgs), a.Model.ContextWindow, s.reserveTokens()) {
			if _, err := s.compact(ctx, "", "threshold"); err != nil && !errors.Is(err, errNothingToCompact) {
				s.emit(Event{Type: "notify", Text: "compaction failed: " + err.Error(), Level: "warning"})
			}
		}
		return nil
	}
	a.Hooks.ToolCall = func(name, callID string, args json.RawMessage) (json.RawMessage, string) {
		if s.Exts == nil {
			return nil, ""
		}
		var input any
		_ = json.Unmarshal(args, &input)
		reply := s.Exts.Emit("tool_call", map[string]any{"toolName": name, "toolCallId": callID, "input": input})
		if b, ok := reply["block"]; ok && string(b) == "true" {
			reason := "blocked by extension"
			if r, ok := reply["reason"]; ok {
				var rs string
				if json.Unmarshal(r, &rs) == nil && rs != "" {
					reason = rs
				}
			}
			return nil, reason
		}
		if in, ok := reply["input"]; ok && len(in) > 0 {
			return in, ""
		}
		return nil, ""
	}
	a.Hooks.ToolResult = func(name, callID string, args json.RawMessage, r tools.Result) tools.Result {
		if s.Exts == nil {
			return r
		}
		var input any
		_ = json.Unmarshal(args, &input)
		reply := s.Exts.Emit("tool_result", map[string]any{"toolName": name, "toolCallId": callID, "input": input, "content": r.Content, "isError": r.IsError})
		if c, ok := reply["content"]; ok {
			var content []ai.Content
			if json.Unmarshal(c, &content) == nil && len(content) > 0 {
				r.Content = content
			}
		}
		if e, ok := reply["isError"]; ok {
			r.IsError = string(e) == "true"
		}
		return r
	}
}

// applyInputHooks lets extensions see and rewrite a user message. A reply with
// "block": true drops the message, which is reported as an empty Role so
// callers can tell "blocked" from "handled".
func (s *Session) applyInputHooks(m ai.Message, queued bool) ai.Message {
	if s.Exts == nil {
		return m
	}
	reply := s.Exts.Emit("input", map[string]any{"text": m.TextContent(), "queued": queued})
	if b, ok := reply["block"]; ok {
		var blocked bool
		if json.Unmarshal(b, &blocked) == nil && blocked {
			reason := "blocked by extension"
			if r, ok := reply["reason"]; ok {
				var rs string
				if json.Unmarshal(r, &rs) == nil && rs != "" {
					reason = rs
				}
			}
			s.emit(Event{Type: "notify", Text: reason, Level: "warning"})
			return ai.Message{}
		}
	}
	if t, ok := reply["text"]; ok {
		var v string
		if json.Unmarshal(t, &v) == nil && v != m.TextContent() {
			// Keep any non-text content, such as images, and replace the text.
			out := ai.UserMessage(v)
			for _, c := range m.Content {
				if c.Type != "text" {
					out.Content = append(out.Content, c)
				}
			}
			return out
		}
	}
	return m
}

// reloadAgentMessages sets the agent's history from the session branch,
// honouring compaction.
func (s *Session) reloadAgentMessages() {
	ctx := s.Store.BuildContext()
	var msgs []ai.Message
	if ctx.Summary != "" {
		msgs = append(msgs, compaction.SummaryMessage(ctx.Summary))
	}
	msgs = append(msgs, ctx.Messages...)
	s.Agent.SetMessages(msgs)
}
