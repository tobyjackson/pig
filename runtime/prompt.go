package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tobyjackson/pig/ai"
)

// Prompt sends text as the user and waits until the agent is idle. If the
// agent is busy the text is queued as a steering message.
func (s *Session) Prompt(ctx context.Context, text string) error {
	return s.PromptMessage(ctx, ai.UserMessage(text))
}

// PromptMessage is Prompt for a message that may carry images.
func (s *Session) PromptMessage(ctx context.Context, m ai.Message) error {
	if s.Agent.IsRunning() {
		s.SteerMessage(m)
		return nil
	}
	if ext := s.applyInputHooks(m, false); ext.Role != "" {
		m = ext
	} else {
		// An extension blocked the prompt.
		return nil
	}
	if s.Exts != nil {
		reply := s.Exts.Emit("before_agent_start", map[string]any{"prompt": m.TextContent(), "systemPrompt": s.Agent.SystemPrompt})
		if sp, ok := reply["systemPrompt"]; ok {
			var v string
			if json.Unmarshal(sp, &v) == nil && v != "" {
				s.Agent.SystemPrompt = v
			}
		}
	}
	err := s.Agent.Prompt(ctx, m)
	if err != nil && isOverflow(err) && s.autoCompaction && ctx.Err() == nil {
		if _, cerr := s.compact(ctx, "", "overflow"); cerr == nil {
			err = s.Agent.Continue(ctx)
		}
	}
	if s.Exts != nil {
		s.Exts.Emit("agent_end", map[string]any{})
	}
	return err
}

func isOverflow(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "prompt is too long") || strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "context window") || strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "maximum context length")
}

// Steer queues text for delivery after the current tool calls.
func (s *Session) Steer(text string) { s.SteerMessage(ai.UserMessage(text)) }

// FollowUp queues text for delivery once the agent is idle.
func (s *Session) FollowUp(text string) { s.FollowUpMessage(ai.UserMessage(text)) }

// SteerMessage and FollowUpMessage queue a message for the agent. The input
// hooks run later, in the agent's goroutine, via Hooks.FilterQueued: a message
// that arrives while the agent is busy used to skip the hooks entirely, and
// running them here would block the caller, which for the TUI is the UI thread.
func (s *Session) SteerMessage(m ai.Message)    { s.Agent.Steer(m) }
func (s *Session) FollowUpMessage(m ai.Message) { s.Agent.FollowUp(m) }

// Abort stops the current run.
func (s *Session) Abort() { s.Agent.Abort() }

// IsRunning reports whether a prompt is in progress.
func (s *Session) IsRunning() bool { return s.Agent.IsRunning() }

// Messages returns the agent's current history (post-compaction view).
func (s *Session) Messages() []ai.Message { return s.Agent.Messages() }

var errNothingToCompact = errors.New("nothing to compact yet")
