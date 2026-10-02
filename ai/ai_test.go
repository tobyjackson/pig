package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadSSE(t *testing.T) {
	in := "event: a\ndata: 1\ndata: 2\n\n: comment\ndata: 3\n\n"
	var got []sseEvent
	readSSE(strings.NewReader(in), func(e sseEvent) bool { got = append(got, e); return true })
	if len(got) != 2 || got[0].Name != "a" || got[0].Data != "1\n2" || got[1].Data != "3" {
		t.Fatalf("got %+v", got)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	m := builtinModels()[0]
	body := buildAnthropicRequest(m, Context{
		SystemPrompt: "sys",
		Messages: []Message{
			UserMessage("hi"),
			{Role: "assistant", Model: m.ID, Content: []Content{
				{Type: "thinking", Thinking: "t", Signature: "sig"},
				{Type: "toolCall", ID: "c1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}},
			{Role: "toolResult", ToolCallID: "c1", Content: []Content{Text("data")}},
			UserMessage("next"),
		},
		Tools: []Tool{{Name: "read", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, Options{ThinkingLevel: "medium"})
	if body["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatal("expected adaptive thinking")
	}
	if body["output_config"].(map[string]any)["effort"] != "medium" {
		t.Fatal("expected effort")
	}
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 4 || msgs[2]["role"] != "user" {
		t.Fatalf("messages: %d", len(msgs))
	}
	tr := msgs[2]["content"].([]map[string]any)[0]
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "c1" {
		t.Fatalf("tool result: %v", tr)
	}
	// Haiku keeps budget_tokens.
	h, _ := (&Registry{Models: builtinModels()}).Find("claude-haiku-4-5")
	hb := buildAnthropicRequest(h, Context{Messages: []Message{UserMessage("x")}}, Options{ThinkingLevel: "low"})
	if hb["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatal("haiku should use budget_tokens")
	}
	// Off is sent explicitly, except where the model cannot turn it off.
	off := buildAnthropicRequest(m, Context{Messages: []Message{UserMessage("x")}}, Options{ThinkingLevel: "off"})
	if off["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("off should send thinking.type=disabled, got %v", off["thinking"])
	}
	f, _ := (&Registry{Models: builtinModels()}).Find("claude-fable-5-1")
	fb := buildAnthropicRequest(f, Context{Messages: []Message{UserMessage("x")}}, Options{ThinkingLevel: "off"})
	if _, ok := fb["thinking"]; ok {
		t.Fatalf("always-on models must not be sent disabled, got %v", fb["thinking"])
	}
}

func TestOpenAIStreamToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer k" {
			t.Errorf("missing auth")
		}
		w.Header().Set("content-type", "text/event-stream")
		lines := []string{
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"f\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`[DONE]`,
		}
		for _, l := range lines {
			w.Write([]byte("data: " + l + "\n\n"))
		}
	}))
	defer srv.Close()
	m := Model{Provider: "test", ID: "m", API: "openai-completions", BaseURL: srv.URL, MaxTokens: 100}
	var final *Message
	var deltas string
	for ev := range Stream(context.Background(), m, Context{Messages: []Message{UserMessage("hi")}}, Options{APIKey: "k"}) {
		switch ev.Type {
		case "text_delta":
			deltas += ev.Delta
		case "done":
			final = ev.Message
		case "error":
			t.Fatalf("error: %s", ev.Message.ErrorMessage)
		}
	}
	if final == nil || deltas != "Hello" || final.StopReason != StopToolUse {
		t.Fatalf("final=%+v deltas=%q", final, deltas)
	}
	calls := final.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || string(calls[0].Arguments) != `{"path":"f"}` {
		t.Fatalf("calls: %+v", calls)
	}
	if final.Usage.Input != 10 || final.Usage.Output != 5 {
		t.Fatalf("usage: %+v", final.Usage)
	}
}

func TestOpenAIThinkingOffSendsNone(t *testing.T) {
	m, _ := (&Registry{Models: builtinModels()}).Find("openrouter/deepseek/deepseek-v4.1-flash")
	// "off" must reach OpenRouter as an explicit effort, otherwise the
	// provider's own default turns thinking back on.
	body := buildOpenAIRequest(m, Context{Messages: []Message{UserMessage("hi")}}, Options{ThinkingLevel: "off"})
	r, ok := body["reasoning"].(map[string]any)
	if !ok || r["effort"] != "none" {
		t.Fatalf("off should send reasoning.effort=none, got %v", body["reasoning"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("openrouter must not use the flat reasoning_effort field")
	}
	// A real level goes through the nested object too.
	body = buildOpenAIRequest(m, Context{Messages: []Message{UserMessage("hi")}}, Options{ThinkingLevel: "low"})
	if body["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("level should map to reasoning.effort, got %v", body["reasoning"])
	}
}

func TestAnthropicStreamAndRetry(t *testing.T) {
	old := retryDelay
	retryDelay = func(int) time.Duration { return time.Millisecond }
	defer func() { retryDelay = old }()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(529)
			w.Write([]byte(`{"error":{"type":"overloaded_error"}}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		evs := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":7,"cache_read_input_tokens":2}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"S"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu1","name":"bash","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		}
		for _, e := range evs {
			w.Write([]byte("event: x\ndata: " + e + "\n\n"))
		}
	}))
	defer srv.Close()
	m := Model{Provider: "anthropic", ID: "claude-opus-5", API: "anthropic-messages", BaseURL: srv.URL, MaxTokens: 100, Reasoning: true, Cost: Cost{Input: 5, Output: 25}}
	retried := false
	var final *Message
	opts := Options{APIKey: "k", ThinkingLevel: "high", OnRetry: func(int, int, time.Duration, error) { retried = true }}
	for ev := range Stream(context.Background(), m, Context{Messages: []Message{UserMessage("hi")}}, opts) {
		if ev.Type == "done" {
			final = ev.Message
		}
		if ev.Type == "error" {
			t.Fatalf("error: %s", ev.Message.ErrorMessage)
		}
	}
	if !retried || calls != 2 {
		t.Fatalf("expected one retry, calls=%d", calls)
	}
	if final == nil || final.StopReason != StopToolUse || len(final.Content) != 2 {
		t.Fatalf("final: %+v", final)
	}
	if final.Content[0].Signature != "S" || final.Content[0].Thinking != "hmm" {
		t.Fatalf("thinking: %+v", final.Content[0])
	}
	if string(final.Content[1].Arguments) != `{"command":"ls"}` {
		t.Fatalf("args: %s", final.Content[1].Arguments)
	}
	if final.Usage.Input != 7 || final.Usage.CacheRead != 2 || final.Usage.Output != 9 || final.Usage.Cost == 0 {
		t.Fatalf("usage: %+v", final.Usage)
	}
}

func TestOpenRouterAnyModelAndCost(t *testing.T) {
	r := &Registry{Models: builtinModels()}
	m, ok := r.Find("openrouter/some-vendor/brand-new-model")
	if !ok || m.Provider != "openrouter" || m.ID != "some-vendor/brand-new-model" || m.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("got %+v %v", m, ok)
	}
	if _, ok := r.Find("openrouter/nope"); ok {
		t.Fatal("ids without a vendor prefix should not be invented")
	}
	body := buildOpenAIRequest(m, Context{Messages: []Message{UserMessage("x")}}, Options{})
	if body["usage"] == nil {
		t.Fatal("openrouter requests should ask for usage.cost")
	}
	u := Usage{Input: 10, Output: 10, Cost: 0.0123}
	ComputeCost(m, &u)
	if u.Cost != 0.0123 || u.TotalTokens != 20 {
		t.Fatalf("reported cost must win: %+v", u)
	}
}

func TestClosestProvider(t *testing.T) {
	r := &Registry{Models: builtinModels(), envKeys: map[string]string{"openrouter": "X"}}
	if got := r.ClosestProvider("openrouteer"); got != "openrouter" {
		t.Fatalf("got %q", got)
	}
	if got := r.ClosestProvider("zzzzzz"); got != "" {
		t.Fatalf("expected no suggestion, got %q", got)
	}
}
