// Package tools holds the built-in tools the model can call: read, write,
// edit, and bash. Each one is small and works on the local file system.
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobyjackson/pig/ai"
)

// Result is what a tool returns. Details is optional extra data kept in the
// session for display, never sent to the model.
type Result struct {
	Content []ai.Content `json:"content"`
	Details any          `json:"details,omitempty"`
	IsError bool         `json:"isError,omitempty"`
}

// TextResult makes a plain text result.
func TextResult(s string) Result { return Result{Content: []ai.Content{ai.Text(s)}} }

// ErrorResult makes an error result the model will see.
func ErrorResult(s string) Result { return Result{Content: []ai.Content{ai.Text(s)}, IsError: true} }

// Tool is one callable capability. Execute may call onUpdate with partial
// output while it runs (used by bash for live output).
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, callID string, args json.RawMessage, onUpdate func(Result)) Result
}

// PromptInfo is optional: a one-line snippet and guideline bullets for the
// system prompt.
type PromptInfo interface {
	PromptSnippet() string
	PromptGuidelines() []string
}

// Definition converts a Tool into the wire form sent to the model.
func Definition(t Tool) ai.Tool {
	return ai.Tool{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()}
}

// writeFileAtomic writes data through a temp file in the same directory and a
// rename, so a crash or a full disk mid-write cannot leave a truncated file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pig-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Resolve makes path absolute against cwd, expanding a leading "~".
func Resolve(path, cwd string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// Defaults returns the four built-in tools rooted at cwd.
func Defaults(cwd string) []Tool {
	return []Tool{NewRead(cwd), NewBash(cwd, ""), NewEdit(cwd), NewWrite(cwd)}
}

// Names lists tool names in order.
func Names(ts []Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

// Filter keeps only tools named in allow (if non-empty) and drops any in exclude.
func Filter(ts []Tool, allow, exclude []string) []Tool {
	has := func(list []string, n string) bool {
		for _, s := range list {
			if s == n {
				return true
			}
		}
		return false
	}
	var out []Tool
	for _, t := range ts {
		if len(allow) > 0 && !has(allow, t.Name()) {
			continue
		}
		if has(exclude, t.Name()) {
			continue
		}
		out = append(out, t)
	}
	return out
}
