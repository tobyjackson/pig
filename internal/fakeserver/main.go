// Command fakeserver pretends to be an OpenAI-compatible model so pig can
// be tried without an API key. First turn: a bash tool call that echoes
// text; next turn: text repeating the tool output. Usage:
//
//	go run ./internal/fakeserver &
//	PIG_DIR=/tmp/pigtest pig --model fake/echo -p "hello"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "listen address")
	flag.Parse()
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		write := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		lastTool := ""
		userText := ""
		for _, m := range req.Messages {
			if m["role"] == "tool" {
				lastTool, _ = m["content"].(string)
			}
			if m["role"] == "user" {
				if parts, ok := m["content"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						userText, _ = p["text"].(string)
					}
				}
			}
		}
		if lastTool == "" {
			write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Let me check. "}}}})
			write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1", "function": map[string]any{"name": "bash", "arguments": `{"command":"echo hi from tool"}`}}}}}}})
			write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}},
				"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
		} else {
			for _, word := range []string{"You said: ", userText, ". The tool said: ", lastTool} {
				write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": word}}}})
			}
			write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 12}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	log.Println("fake model server on", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
