package compaction

import (
	"strings"
	"testing"

	"github.com/tobyjackson/pig/ai"
)

func TestCutIndexStartsAtUserMessage(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens
	msgs := []ai.Message{
		ai.UserMessage(big), {Role: "assistant", Content: []ai.Content{ai.Text(big)}},
		ai.UserMessage(big), {Role: "assistant", Content: []ai.Content{{Type: "toolCall", Name: "read", Arguments: []byte(`{}`)}}},
		{Role: "toolResult", ToolName: "read", Content: []ai.Content{ai.Text(big)}},
		{Role: "assistant", Content: []ai.Content{ai.Text(big)}},
	}
	cut := CutIndex(msgs, 2500)
	if cut != 2 || msgs[cut].Role != "user" {
		t.Fatalf("cut=%d role=%s", cut, msgs[cut].Role)
	}
	// Everything fits: still cut at the latest user message so we shrink.
	if c := CutIndex(msgs, 1_000_000); c != 2 {
		t.Fatalf("fits-all cut=%d", c)
	}
}

func TestShouldCompact(t *testing.T) {
	if ShouldCompact(100, 1000, 100) || !ShouldCompact(950, 1000, 100) {
		t.Fatal("threshold wrong")
	}
}

func TestSerializeTruncatesToolResults(t *testing.T) {
	msgs := []ai.Message{{Role: "toolResult", ToolName: "bash", Content: []ai.Content{ai.Text(strings.Repeat("y", 5000))}}}
	s := Serialize(msgs)
	if !strings.Contains(s, "[truncated]") || len(s) > 2200 {
		t.Fatalf("len=%d", len(s))
	}
}
