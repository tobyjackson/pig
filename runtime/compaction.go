package runtime

import (
	"context"
	"errors"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/compaction"
)

// AutoCompaction reports whether automatic compaction is on.
func (s *Session) AutoCompaction() bool { return s.autoCompaction }

// SetAutoCompaction turns automatic compaction on or off.
func (s *Session) SetAutoCompaction(on bool) { s.autoCompaction = on }

// Compact summarises older history now. custom adds focus instructions.
func (s *Session) Compact(ctx context.Context, custom string) (string, error) {
	return s.compact(ctx, custom, "manual")
}

// reserveTokens is the configured reserve, shrunk for small context windows
// so the threshold can never be negative.
func (s *Session) reserveTokens() int {
	r := s.Settings.Compaction.ReserveTokens
	if w := s.Agent.Model.ContextWindow; w > 0 && r > w/2 {
		r = w / 4
	}
	return r
}

// compactionBudget is the largest kept part compaction may leave behind. The
// next request adds the system prompt, the tools and the reply, so the kept part
// and the reserve have to fit under the window together. A floor keeps the
// figure sane when a model reports a tiny or missing context window.
func (s *Session) compactionBudget() int {
	w := s.Agent.Model.ContextWindow
	if w <= 0 {
		return s.Settings.Compaction.KeepRecentTokens
	}
	b := w - s.reserveTokens() - len(s.Agent.SystemPrompt)/4
	if b < s.Settings.Compaction.KeepRecentTokens {
		// KeepRecentTokens is the user's stated preference; never override it
		// downward, or compaction would keep less than asked for.
		return s.Settings.Compaction.KeepRecentTokens
	}
	return b
}

// compact summarises the older part of the history and records it. reason is
// "manual", "threshold", or "overflow".
func (s *Session) compact(ctx context.Context, custom, reason string) (string, error) {
	s.mu.Lock()
	if s.compacting {
		s.mu.Unlock()
		return "", errors.New("compaction already running")
	}
	s.compacting = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.compacting = false; s.mu.Unlock() }()

	bctx := s.Store.BuildContext()
	msgs := bctx.Messages
	cut := compaction.CutIndex(msgs, s.Settings.Compaction.KeepRecentTokens, s.compactionBudget())
	if cut <= 0 || cut >= len(msgs) {
		return "", errNothingToCompact
	}
	s.emit(Event{Type: "compaction_start", Reason: reason})
	toSummarize := msgs[:cut]
	kept := msgs[cut:]
	before := compaction.ContextTokens(s.Agent.SystemPrompt, s.Agent.Messages())
	summary, usage, err := compaction.Summarize(ctx, s.Agent.Stream, s.Agent.Model, s.Registry.APIKey(s.Agent.Model.Provider), toSummarize, bctx.Summary, custom)
	if err != nil {
		s.emit(Event{Type: "compaction_end", Reason: reason, Error: err.Error()})
		return "", err
	}
	firstKept := ""
	if cut < len(bctx.EntryIDs) {
		firstKept = bctx.EntryIDs[cut]
	}
	s.Store.AppendCompaction(summary, firstKept, before, usage)
	var newMsgs []ai.Message
	newMsgs = append(newMsgs, compaction.SummaryMessage(summary))
	newMsgs = append(newMsgs, kept...)
	s.Agent.SetMessages(newMsgs)
	s.emit(Event{Type: "compaction_end", Reason: reason, Summary: summary})
	return summary, nil
}
