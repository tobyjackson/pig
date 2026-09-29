package tools

// The tool description, JSON schema and prompt snippet are ported from pi:
// packages/coding-agent/src/core/tools/read.ts
// Copyright (c) 2025 Mario Zechner, MIT licensed. See LICENSE.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobyjackson/pig/ai"
)

// Read returns a file's text, or an image as an attachment.
type Read struct{ cwd string }

// NewRead makes the read tool rooted at cwd.
func NewRead(cwd string) *Read { return &Read{cwd: cwd} }

func (*Read) Name() string { return "read" }
func (*Read) Description() string {
	return fmt.Sprintf("Read the contents of a file. Supports text files and images (jpg, png, gif, webp). Images are sent as attachments. For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.", MaxLines, MaxBytes/1024)
}
func (*Read) PromptSnippet() string { return "Read file contents" }
func (*Read) PromptGuidelines() []string {
	return []string{"Use read to examine files instead of cat or sed."}
}
func (*Read) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},
"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},
"limit":{"type":"number","description":"Maximum number of lines to read"}},
"required":["path"],"additionalProperties":false}`)
}

var imageMimes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

func (r *Read) Execute(ctx context.Context, _ string, args json.RawMessage, _ func(Result)) Result {
	var in struct {
		Path   string  `json:"path"`
		Offset float64 `json:"offset"`
		Limit  float64 `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" {
		return ErrorResult("read: a path is required")
	}
	abs := Resolve(in.Path, r.cwd)
	data, err := os.ReadFile(abs)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Could not read file: %s. %v", in.Path, err))
	}
	if mime, ok := imageMimes[strings.ToLower(filepath.Ext(abs))]; ok {
		return Result{Content: []ai.Content{
			ai.Text("Read image file [" + mime + "]"),
			{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
		}}
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	total := len(lines)
	start := 0
	if in.Offset > 0 {
		start = int(in.Offset) - 1
	}
	if start >= total {
		return ErrorResult(fmt.Sprintf("Offset %d is beyond end of file (%d lines total)", int(in.Offset), total))
	}
	end := total
	userLimited := false
	if in.Limit > 0 && start+int(in.Limit) < total {
		end = start + int(in.Limit)
		userLimited = true
	}
	selected := strings.Join(lines[start:end], "\n")
	t := TruncateHead(selected)
	startDisp := start + 1
	var out string
	switch {
	case t.FirstLineExceedsLimit:
		out = fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startDisp, FormatSize(len(lines[start])), FormatSize(MaxBytes), startDisp, in.Path, MaxBytes)
	case t.Truncated:
		endDisp := startDisp + t.OutputLines - 1
		note := fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", startDisp, endDisp, total, endDisp+1)
		if t.TruncatedBy == "bytes" {
			note = fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]", startDisp, endDisp, total, FormatSize(MaxBytes), endDisp+1)
		}
		out = t.Content + note
	case userLimited:
		out = fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]", t.Content, total-end, end+1)
	default:
		out = t.Content
	}
	res := TextResult(out)
	if t.Truncated {
		res.Details = map[string]any{"truncation": t}
	}
	return res
}
