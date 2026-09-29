package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/tools"
)

// fakeStream answers a tool call first, then plain text.
func fakeStream(calls *int) ai.StreamFunc {
	return func(ctx context.Context, m ai.Model, c ai.Context, o ai.Options) <-chan ai.Event {
		out := make(chan ai.Event, 8)
		go func() {
			defer close(out)
			*calls++
			msg := &ai.Message{Role: "assistant", Usage: &ai.Usage{}, Timestamp: ai.Now()}
			out <- ai.Event{Type: "start", Partial: msg}
			var last *ai.Message
			for i := range c.Messages {
				if c.Messages[i].Role == "toolResult" {
					last = &c.Messages[i]
				}
			}
			if last != nil {
				msg.Content = []ai.Content{ai.Text("done: " + last.TextContent())}
				msg.StopReason = ai.StopStop
				out <- ai.Event{Type: "text_delta", Delta: msg.Content[0].Text, Partial: msg}
			} else {
				msg.Content = []ai.Content{{Type: "toolCall", ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)}}
				msg.StopReason = ai.StopToolUse
			}
			out <- ai.Event{Type: "done", Reason: msg.StopReason, Message: msg}
		}()
		return out
	}
}

type echoTool struct{}

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "echo" }
func (echoTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (echoTool) Execute(_ context.Context, _ string, args json.RawMessage, _ func(tools.Result)) tools.Result {
	var in struct{ Text string }
	json.Unmarshal(args, &in)
	return tools.TextResult(in.Text)
}

func TestLoopRunsToolThenStops(t *testing.T) {
	calls := 0
	a := New()
	a.Stream = fakeStream(&calls)
	a.Tools = []tools.Tool{echoTool{}}
	var types []string
	a.Subscribe(func(e Event) { types = append(types, e.Type) })
	blocked := ""
	a.Hooks.ToolCall = func(name, id string, args json.RawMessage) (json.RawMessage, string) {
		return json.RawMessage(`{"text":"changed"}`), blocked
	}
	if err := a.Prompt(context.Background(), ai.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	msgs := a.Messages()
	if len(msgs) != 4 || msgs[3].TextContent() != "done: changed" {
		t.Fatalf("messages: %d last=%q", len(msgs), msgs[len(msgs)-1].TextContent())
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	want := map[string]bool{"agent_start": false, "tool_execution_start": false, "tool_execution_end": false, "agent_end": false}
	for _, ty := range types {
		if _, ok := want[ty]; ok {
			want[ty] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Fatalf("missing event %s", k)
		}
	}
}

func TestSteeringDelivered(t *testing.T) {
	calls := 0
	a := New()
	a.Stream = fakeStream(&calls)
	a.Tools = []tools.Tool{echoTool{}}
	steered := false
	a.Subscribe(func(e Event) {
		if e.Type == "tool_execution_start" && !steered {
			steered = true
			a.Steer(ai.UserMessage("also this"))
		}
	})
	a.Prompt(context.Background(), ai.UserMessage("go"))
	found := false
	for _, m := range a.Messages() {
		if m.Role == "user" && m.TextContent() == "also this" {
			found = true
		}
	}
	if !found {
		t.Fatal("steering message not delivered")
	}
}
