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

// CutIndex picks where kept messages begin: walk back from the end
// collecting about keepRecent tokens, then move to the start of that turn
// (a user message). Returns len(msgs) when nothing older exists to cut.
func CutIndex(msgs []ai.Message, keepRecent int) int {
	if len(msgs) == 0 {
		return 0
	}
	total := 0
	i := len(msgs) - 1
	for ; i >= 0; i-- {
		total += EstimateTokens(msgs[i])
		if total >= keepRecent {
			break
		}
	}
	if i < 0 {
		i = 0
	}
	// Move to the nearest user message at or before i, so no tool result is
	// separated from its call.
	for i > 0 && msgs[i].Role != "user" {
		i--
	}
	if i == 0 {
		// Everything fits in keepRecent, or the first turn is huge. Cut at
		// the most recent user message instead so we still shrink.
		for j := len(msgs) - 1; j > 0; j-- {
			if msgs[j].Role == "user" {
				return j
			}
		}
		return 0
	}
	return i
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
