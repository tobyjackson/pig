// Package ai is the model layer: one message format, streaming events,
// and providers that translate to and from vendor HTTP APIs.
package ai

import (
	"encoding/json"
	"time"
)

// Content is one block inside a message. Type decides which fields matter:
// "text", "thinking", "image", or "toolCall".
type Content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Redacted  bool            `json:"redacted,omitempty"`
	Data      string          `json:"data,omitempty"`     // base64 image bytes
	MimeType  string          `json:"mimeType,omitempty"` // image/png etc.
	ID        string          `json:"id,omitempty"`       // tool call id
	Name      string          `json:"name,omitempty"`     // tool name
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Text makes a text content block.
func Text(s string) Content { return Content{Type: "text", Text: s} }

// Usage counts tokens and cost for one model response.
type Usage struct {
	Input       int     `json:"input"`
	Output      int     `json:"output"`
	CacheRead   int     `json:"cacheRead"`
	CacheWrite  int     `json:"cacheWrite"`
	TotalTokens int     `json:"totalTokens"`
	Cost        float64 `json:"cost"`
}

// Add sums another usage into this one.
func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.TotalTokens += o.TotalTokens
	u.Cost += o.Cost
}

// Stop reasons for an assistant message.
const (
	StopPending = "pending"
	StopStop    = "stop"
	StopLength  = "length"
	StopToolUse = "toolUse"
	StopError   = "error"
	StopAborted = "aborted"
)

// Message is one turn. Role is "user", "assistant", or "toolResult".
type Message struct {
	Role      string    `json:"role"`
	Content   []Content `json:"content"`
	Timestamp int64     `json:"timestamp"`

	// Assistant fields.
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	StopReason   string `json:"stopReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Tool result fields.
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
}

// Now returns the current time in milliseconds, the timestamp unit used everywhere.
func Now() int64 { return time.Now().UnixMilli() }

// UserMessage makes a plain text user message.
func UserMessage(text string) Message {
	return Message{Role: "user", Content: []Content{Text(text)}, Timestamp: Now()}
}

// TextContent joins all text blocks of a message.
func (m Message) TextContent() string {
	var out string
	for _, c := range m.Content {
		if c.Type == "text" {
			out += c.Text
		}
	}
	return out
}

// ToolCalls returns the tool call blocks of an assistant message.
func (m Message) ToolCalls() []Content {
	var out []Content
	for _, c := range m.Content {
		if c.Type == "toolCall" {
			out = append(out, c)
		}
	}
	return out
}

// Tool describes a function the model may call. Parameters is a JSON schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Context is everything sent to the model for one request.
type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

// Event is one streaming update from a provider. Types: start, text_start,
// text_delta, text_end, thinking_start, thinking_delta, thinking_end,
// toolcall_start, toolcall_delta, toolcall_end, done, error.
type Event struct {
	Type     string   `json:"type"`
	Index    int      `json:"contentIndex,omitempty"`
	Delta    string   `json:"delta,omitempty"`
	Content  string   `json:"content,omitempty"`
	ToolCall *Content `json:"toolCall,omitempty"`
	Partial  *Message `json:"partial,omitempty"`
	Message  *Message `json:"message,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// ThinkingLevel values, lowest to highest. "off" disables thinking.
var ThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidThinkingLevel reports whether s is a known level.
func ValidThinkingLevel(s string) bool {
	for _, l := range ThinkingLevels {
		if l == s {
			return true
		}
	}
	return false
}
