package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Write creates or overwrites a file, making parent folders as needed.
type Write struct{ cwd string }

// NewWrite makes the write tool rooted at cwd.
func NewWrite(cwd string) *Write { return &Write{cwd: cwd} }

func (*Write) Name() string { return "write" }
func (*Write) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories."
}
func (*Write) PromptSnippet() string { return "Create or overwrite files" }
func (*Write) PromptGuidelines() []string {
	return []string{"Use write only for new files or complete rewrites."}
}
func (*Write) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},
"content":{"type":"string","description":"Content to write to the file"}},
"required":["path","content"],"additionalProperties":false}`)
}

func (w *Write) Execute(ctx context.Context, _ string, args json.RawMessage, _ func(Result)) Result {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" {
		return ErrorResult("write: path and content are required")
	}
	abs := Resolve(in.Path, w.cwd)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return ErrorResult(fmt.Sprintf("Could not create directory: %v", err))
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("Could not write file: %v", err))
	}
	return TextResult("Successfully wrote to " + in.Path)
}
