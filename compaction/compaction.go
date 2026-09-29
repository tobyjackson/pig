// Package compaction shrinks a long conversation. Older messages are
// summarised by the model; recent ones are kept as they are.
package compaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobyjackson/pig/ai"
)

// EstimateTokens guesses tokens from characters (about 4 per token).
func EstimateTokens(m ai.Message) int {
	n := 0
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			n += len(c.Text)
		case "thinking":
			n += len(c.Thinking)
		case "toolCall":
			n += len(c.Name) + len(c.Arguments)
		case "image":
			n += 6000 // a rough per-image cost in characters
		}
	}
	return n/4 + 4
}

// EstimateContext sums estimates for all messages plus the system prompt.
func EstimateContext(system string, msgs []ai.Message) int {
	n := len(system) / 4
	for _, m := range msgs {
		n += EstimateTokens(m)
	}
	return n
}

// ContextTokens is the best known size of the next request: the last
// assistant message's usage when we have one, else an estimate.
func ContextTokens(system string, msgs []ai.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && m.Usage != nil && m.Usage.Input+m.Usage.CacheRead > 0 {
			total := m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheWrite + m.Usage.Output
			// Add anything that came after that response.
			for _, later := range msgs[i+1:] {
				total += EstimateTokens(later)
			}
			return total
		}
	}
	return EstimateContext(system, msgs)
}

// ShouldCompact reports whether the context is too close to the window.
func ShouldCompact(contextTokens, contextWindow, reserveTokens int) bool {
	return contextTokens > contextWindow-reserveTokens
}

// CutIndex picks where the kept messages begin.
//
// keepRecent is how much tail to keep when there is room. budget is the largest
// kept part allowed: if the kept part is bigger than budget, compaction has not
// shrunk the context enough and the next request may still fail.
//
// The cut always starts on a user message or an assistant message, never on a
// tool result, so a tool result stays with the call it answers. A user message is
// preferred, which keeps the current turn whole. An assistant message is the
// fallback, which is what a long run of tool calls and results needs: when one
// turn's tool run is larger than the budget there is no turn-boundary cut that
// fits, and cutting mid-turn is cheaper than refusing to compact.
//
// The returned cut is always one whose kept part fits budget, so compaction
// cannot loop: after it runs, ShouldCompact is false and it will not fire again
// on the same history. Returns 0 when there is nothing older to cut.
func CutIndex(msgs []ai.Message, keepRecent, budget int) int {
	n := len(msgs)
	if n == 0 {
		return 0
	}
	if budget <= 0 {
		// No usable window to respect; fall back to keepRecent alone.
		budget = 1 << 30
	}

	// Preferred: the last user message, which keeps the current turn whole. Only
	// when it fits, so the guarantee below still holds.
	if j := lastUser(msgs); j > 0 && keptFits(msgs, j, budget) {
		return j
	}

	// Walk back from the end until the budget point, then cut at the first
	// message at or after it that can start the kept part. Since the kept part
	// shrinks as j grows, the first fitting j is the largest kept part allowed.
	point := 1
	total := 0
	for i := n - 1; i > 0; i-- {
		total += EstimateTokens(msgs[i])
		if total >= keepRecent {
			point = i
			break
		}
	}
	for j := point; j < n; j++ {
		if msgs[j].Role == "toolResult" {
			continue
		}
		if keptFits(msgs, j, budget) {
			return j
		}
	}

	// Only tool results from the budget point on, and none of them fit. Walk back
	// to the call they answer rather than splitting one from the other.
	for j := point; j > 0; j-- {
		if msgs[j].Role != "toolResult" {
			return j
		}
	}
	return 0
}

// lastUser returns the index of the last user message, or -1.
func lastUser(msgs []ai.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

// keptFits reports whether keeping msgs[cut:] stays within budget.
func keptFits(msgs []ai.Message, cut, budget int) bool {
	total := 0
	for _, m := range msgs[cut:] {
		total += EstimateTokens(m)
		if total > budget {
			return false
		}
	}
	return true
}

// Serialize renders messages as plain text for the summariser.
func Serialize(msgs []ai.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			sb.WriteString("[User]: " + m.TextContent() + "\n\n")
		case "assistant":
			var parts []string
			if t := m.TextContent(); t != "" {
				parts = append(parts, t)
			}
			for _, tc := range m.ToolCalls() {
				parts = append(parts, fmt.Sprintf("[Tool call: %s %s]", tc.Name, string(tc.Arguments)))
			}
			sb.WriteString("[Assistant]: " + strings.Join(parts, "\n") + "\n\n")
		case "toolResult":
			t := m.TextContent()
			if len(t) > 2000 {
				t = t[:2000] + "\n[truncated]"
			}
			sb.WriteString("[Tool result: " + m.ToolName + "]: " + t + "\n\n")
		}
	}
	return sb.String()
}

const summaryFormat = `## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// SummaryPrompt is the instruction for a first summary.
const SummaryPrompt = "The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.\n\nUse this EXACT format:\n\n" + summaryFormat

// UpdatePrompt is used when an earlier summary exists.
const UpdatePrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

` + summaryFormat

// Summarize asks the model for a summary of msgs. previous may be empty.
// custom adds extra focus instructions from the user.
func Summarize(ctx context.Context, stream ai.StreamFunc, m ai.Model, apiKey string, msgs []ai.Message, previous, custom string) (string, *ai.Usage, error) {
	var sb strings.Builder
	sb.WriteString("<conversation>\n" + Serialize(msgs) + "</conversation>\n\n")
	if previous != "" {
		sb.WriteString("<previous-summary>\n" + previous + "\n</previous-summary>\n\n")
		sb.WriteString(UpdatePrompt)
	} else {
		sb.WriteString(SummaryPrompt)
	}
	if custom != "" {
		sb.WriteString("\n\nAdditional focus from the user: " + custom)
	}
	req := ai.Context{Messages: []ai.Message{ai.UserMessage(sb.String())}}
	var final *ai.Message
	for ev := range stream(ctx, m, req, ai.Options{APIKey: apiKey, ThinkingLevel: "off", MaxTokens: 8192}) {
		if ev.Type == "done" || ev.Type == "error" {
			final = ev.Message
		}
	}
	if final == nil {
		return "", nil, fmt.Errorf("summarization returned nothing")
	}
	if final.StopReason == ai.StopError || final.StopReason == ai.StopAborted {
		return "", final.Usage, fmt.Errorf("summarization failed: %s", final.ErrorMessage)
	}
	if final.StopReason == ai.StopLength {
		return "", final.Usage, fmt.Errorf("summarization hit the token cap; summary incomplete")
	}
	text := strings.TrimSpace(final.TextContent())
	if text == "" {
		return "", final.Usage, fmt.Errorf("summarization produced no text")
	}
	return text, final.Usage, nil
}

// SummaryMessage wraps a summary as the user message that opens a compacted context.
func SummaryMessage(summary string) ai.Message {
	m := ai.UserMessage("The conversation so far was summarized to save space. Continue from this summary; the full history is still in the session file.\n\n<summary>\n" + summary + "\n</summary>")
	return m
}
