package compaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tobyjackson/pig/ai"
)

func TestCutIndexKeepsCurrentTurn(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens
	msgs := []ai.Message{
		ai.UserMessage(big), {Role: "assistant", Content: []ai.Content{ai.Text(big)}},
		ai.UserMessage(big), {Role: "assistant", Content: []ai.Content{{Type: "toolCall", Name: "read", Arguments: []byte(`{}`)}}},
		{Role: "toolResult", ToolName: "read", Content: []ai.Content{ai.Text(big)}},
		{Role: "assistant", Content: []ai.Content{ai.Text(big)}},
	}
	cut := CutIndex(msgs, 2500, 10_000)
	if cut != 2 || msgs[cut].Role != "user" {
		t.Fatalf("cut=%d role=%s", cut, msgs[cut].Role)
	}
	// The whole history fits the budget: keep the latest user message so the
	// session still shrinks when compacted manually.
	if c := CutIndex(msgs, 1_000_000, 1_000_000); c != 2 {
		t.Fatalf("fits-all cut=%d", c)
	}
}

// A run of tool calls and results with no user message near the end used to make
// CutIndex return 0, which left the context over the window and made the next
// request fail. It must cut mid-turn instead.
func TestCutIndexSplitsOversizedToolRun(t *testing.T) {
	msgs := []ai.Message{ai.UserMessage("start")}
	for i := 0; i < 30; i++ {
		tag := []byte(`{"command":"rg"}`)
		msgs = append(msgs, ai.Message{Role: "assistant", Content: []ai.Content{
			{Type: "toolCall", Name: "bash", ID: fmt.Sprintf("c%d", i), Arguments: tag}}})
		msgs = append(msgs, ai.Message{Role: "toolResult", ToolName: "bash",
			Content: []ai.Content{ai.Text(strings.Repeat("x", 50_000))}})
	}
	const budget = 20_000
	cut := CutIndex(msgs, budget, budget)
	if cut <= 0 {
		t.Fatalf("cut=%d: refused to compact a tool run", cut)
	}
	if msgs[cut].Role == "toolResult" {
		t.Fatalf("cut=%d starts on a tool result", cut)
	}
	// The kept part must fit the budget, or the next compaction round repeats
	// this cut and the context never shrinks.
	if kept := ContextTokens("", msgs[cut:]); kept > budget {
		t.Fatalf("kept=%d exceeds budget=%d", kept, budget)
	}
}

// Compaction must converge: after a cut, ShouldCompact is false, so it does not
// fire again on the same history. This is the loop that used to burn a
// summarisation call per turn and remove nothing.
func TestCutIndexDoesNotLoop(t *testing.T) {
	msgs := []ai.Message{ai.UserMessage("start")}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, ai.Message{Role: "assistant", Content: []ai.Content{
			{Type: "toolCall", Name: "bash", ID: fmt.Sprintf("c%d", i), Arguments: []byte(`{}`)}}})
		msgs = append(msgs, ai.Message{Role: "toolResult", ToolName: "bash",
			Content: []ai.Content{ai.Text(strings.Repeat("x", 50_000))}})
	}
	const window, reserve = 200_000, 16_384
	budget := window - reserve
	if !ShouldCompact(EstimateContext("", msgs), window, reserve) {
		t.Fatal("setup: history should be over the limit")
	}
	cut := CutIndex(msgs, 20_000, budget)
	if cut <= 0 {
		t.Fatal("nothing to compact")
	}
	kept := append([]ai.Message{{Role: "user", Content: []ai.Content{ai.Text("[summary]")}}}, msgs[cut:]...)
	if ShouldCompact(EstimateContext("", kept), window, reserve) {
		t.Fatalf("still over the limit after compaction: %d tokens", EstimateContext("", kept))
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
