package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyjackson/pig/ai"
)

// scriptedStream answers each request with the next reply in the list.
// A reply beginning with "tool:" becomes a bash tool call.
func scriptedStream(replies []string) ai.StreamFunc {
	i := 0
	return func(ctx context.Context, m ai.Model, c ai.Context, o ai.Options) <-chan ai.Event {
		out := make(chan ai.Event, 8)
		go func() {
			defer close(out)
			reply := "done"
			if i < len(replies) {
				reply = replies[i]
			}
			i++
			msg := &ai.Message{Role: "assistant", Provider: m.Provider, Model: m.ID, Usage: &ai.Usage{Input: 100, Output: 10}, Timestamp: ai.Now()}
			out <- ai.Event{Type: "start", Partial: msg}
			if cmd, ok := strings.CutPrefix(reply, "tool:"); ok {
				args, _ := json.Marshal(map[string]string{"command": cmd})
				msg.Content = []ai.Content{{Type: "toolCall", ID: "c1", Name: "bash", Arguments: args}}
				msg.StopReason = ai.StopToolUse
			} else {
				msg.Content = []ai.Content{ai.Text(reply)}
				msg.StopReason = ai.StopStop
				out <- ai.Event{Type: "text_delta", Delta: reply, Partial: msg}
			}
			out <- ai.Event{Type: "done", Reason: msg.StopReason, Message: msg}
		}()
		return out
	}
}

func setup(t *testing.T, replies []string) *Session {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Always be brief."), 0o644)
	s, err := New(Options{Cwd: t.TempDir(), Model: "claude-opus-5", Stream: scriptedStream(replies), Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPromptWithToolAndSessionFile(t *testing.T) {
	s := setup(t, []string{"tool:echo from-tool", "all good"})
	if !strings.Contains(s.Agent.SystemPrompt, "Always be brief.") {
		t.Fatal("context file missing from system prompt")
	}
	if err := s.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if got := s.LastAssistantText(); got != "all good" {
		t.Fatalf("got %q", got)
	}
	msgs := s.Store.Messages()
	if len(msgs) != 4 || msgs[2].Role != "toolResult" || !strings.Contains(msgs[2].TextContent(), "from-tool") {
		t.Fatalf("session messages: %d", len(msgs))
	}
	if _, err := os.Stat(s.Store.Path()); err != nil {
		t.Fatal("session file not written")
	}
	st := s.Stats()
	if st.ToolCalls != 1 || st.Tokens.Input != 200 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestManualCompactionAndReload(t *testing.T) {
	s := setup(t, []string{"one", "two", "three", "## Goal\nsummary here", "four"})
	ctx := context.Background()
	for _, p := range []string{"a", "b", "c"} {
		if err := s.Prompt(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	s.Settings.Compaction.KeepRecentTokens = 1 // keep only the last turn
	summary, err := s.Compact(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "summary here") {
		t.Fatalf("summary: %q", summary)
	}
	msgs := s.Messages()
	if len(msgs) != 3 || !strings.Contains(msgs[0].TextContent(), "summary here") || msgs[1].TextContent() != "c" {
		t.Fatalf("post-compaction context wrong: %d msgs, first=%q", len(msgs), msgs[0].TextContent())
	}
	// Reopen the file: the same view must come back.
	re, err := New(Options{Cwd: s.Opts.Cwd, Model: "claude-opus-5", SessionPath: s.Store.Path(), Stream: scriptedStream(nil), Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Messages(); len(got) != 3 || got[1].TextContent() != "c" {
		t.Fatalf("reloaded context wrong: %d", len(got))
	}
	if err := s.Prompt(ctx, "d"); err != nil || s.LastAssistantText() != "four" {
		t.Fatalf("after compaction: %v %q", err, s.LastAssistantText())
	}
}

func TestForkAndTree(t *testing.T) {
	s := setup(t, []string{"r1", "r2", "r3"})
	ctx := context.Background()
	s.Prompt(ctx, "first")
	s.Prompt(ctx, "second")
	points := s.ForkPoints()
	if len(points) != 2 {
		t.Fatalf("fork points: %d", len(points))
	}
	old := s.Store.Path()
	text, err := s.Fork(points[1].EntryID)
	if err != nil || text != "second" {
		t.Fatalf("fork: %v %q", err, text)
	}
	if s.Store.Path() == old || len(s.Messages()) != 2 {
		t.Fatalf("fork should make a new file with 2 messages, got %d", len(s.Messages()))
	}
	if err := s.Prompt(ctx, "second, revised"); err != nil || s.LastAssistantText() != "r3" {
		t.Fatalf("after fork: %v", err)
	}
}

func TestPromptTemplateAndSkillExpansion(t *testing.T) {
	s := setup(t, nil)
	dir := os.Getenv("PIG_DIR")
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "fix.md"), []byte("Fix $1 please"), 0o644)
	os.MkdirAll(filepath.Join(dir, "skills", "greet"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "greet", "SKILL.md"), []byte("---\nname: greet\ndescription: Greet people\n---\nSay hi."), 0o644)
	s.Reload()
	text, handled, err := s.ExpandInput("/fix main.go")
	if err != nil || !handled || text != "Fix main.go please" {
		t.Fatalf("template: %q %v %v", text, handled, err)
	}
	text, handled, err = s.ExpandInput("/skill:greet Bob")
	if err != nil || !handled || !strings.Contains(text, "Say hi.") || !strings.HasSuffix(text, "Bob") {
		t.Fatalf("skill: %q %v %v", text, handled, err)
	}
	if !strings.Contains(s.Agent.SystemPrompt, "<name>greet</name>") {
		t.Fatal("skill not in system prompt")
	}
	if _, handled, _ := s.ExpandInput("plain text"); handled {
		t.Fatal("plain text should not be handled")
	}
}
