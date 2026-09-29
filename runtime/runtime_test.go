package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/compaction"
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

// Warnings raised while the session is being built must survive until a
// listener attaches, or a malformed skill or template says nothing at all.
func TestResourceWarningsReachSubscriber(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "broken.md"),
		[]byte("---\ndescription is missing its colon\n---\nBody"), 0o644)

	s, err := New(Options{Cwd: t.TempDir(), Model: "claude-opus-5", Stream: scriptedStream(nil), Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	s.Subscribe(func(e Event) {
		if e.Type == "notify" {
			got = append(got, e.Text)
		}
	})
	if len(got) == 0 {
		t.Fatal("warning raised during New was dropped")
	}
	if !strings.Contains(strings.Join(got, " "), "key: value") {
		t.Fatalf("unhelpful warning: %v", got)
	}
}

// A steer or follow-up must pass the input hooks too. It used to reach the agent
// queue directly, so an extension that rewrites prompts never saw it.
func TestQueuedMessagePassesInputHooks(t *testing.T) {
	ext, err := filepath.Abs("../examples/extensions/hello.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	s, err := New(Options{Cwd: t.TempDir(), Model: "claude-opus-5", Stream: scriptedStream(nil), Mode: "print", ExtensionPaths: []string{ext}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The hook rewrites "? why is the sky blue" to "why is the sky blue".
	rewritten, ok := s.Agent.Hooks.FilterQueued(ai.UserMessage("? why is the sky blue"))
	if !ok || rewritten.TextContent() != "why is the sky blue" {
		t.Fatalf("queued message not rewritten: ok=%v text=%q", ok, rewritten.TextContent())
	}
	// And drops "!drop".
	if _, ok := s.Agent.Hooks.FilterQueued(ai.UserMessage("!drop")); ok {
		t.Fatal("queued message should have been dropped")
	}
	// Plain text passes through unchanged.
	plain, ok := s.Agent.Hooks.FilterQueued(ai.UserMessage("keep me"))
	if !ok || plain.TextContent() != "keep me" {
		t.Fatalf("plain message altered: ok=%v text=%q", ok, plain.TextContent())
	}
}

// Compaction must actually shrink an oversized tool run. Before this, a turn
// whose tool calls and results were larger than the budget made CutIndex return
// 0, compact() returned errNothingToCompact, and the next request failed because
// the context was still over the window.
func TestCompactShrinksOversizedToolRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	// The first scripted reply is consumed by the summarisation call.
	s, err := New(Options{Cwd: t.TempDir(), Model: "claude-haiku-4-5",
		Stream: scriptedStream([]string{"## Goal\nsummary of the tool run"}), Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	big := strings.Repeat("x", 50_000)
	// compact() builds context from the session store, so the history has to go
	// there, not just into the agent.
	s.Store.AppendMessage(ai.UserMessage("start"))
	for i := 0; i < 30; i++ {
		s.Store.AppendMessage(ai.Message{Role: "assistant", Content: []ai.Content{
			{Type: "toolCall", Name: "bash", ID: fmt.Sprintf("c%d", i), Arguments: []byte(`{}`)}}})
		s.Store.AppendMessage(ai.Message{Role: "toolResult", ToolName: "bash",
			Content: []ai.Content{ai.Text(big)}})
	}
	s.reloadAgentMessages()

	before := compaction.ContextTokens(s.Agent.SystemPrompt, s.Agent.Messages())
	if !compaction.ShouldCompact(before, s.Model().ContextWindow, s.reserveTokens()) {
		t.Fatalf("setup: %d tokens is not over the limit", before)
	}
	if _, err := s.Compact(context.Background(), ""); err != nil {
		t.Fatalf("compaction refused: %v", err)
	}
	after := compaction.ContextTokens(s.Agent.SystemPrompt, s.Agent.Messages())
	if compaction.ShouldCompact(after, s.Model().ContextWindow, s.reserveTokens()) {
		t.Fatalf("still over the limit after compaction: %d -> %d tokens", before, after)
	}
}
