package tools

// The tool description, JSON schema and prompt snippet are ported from pi:
// packages/coding-agent/src/core/tools/edit.ts
// Copyright (c) 2025 Mario Zechner, MIT licensed. See LICENSE.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Edit replaces exact text in one file. Several replacements may be sent in
// one call; each is matched against the original file.
type Edit struct{ cwd string }

// NewEdit makes the edit tool rooted at cwd.
func NewEdit(cwd string) *Edit { return &Edit{cwd: cwd} }

func (*Edit) Name() string { return "edit" }
func (*Edit) Description() string {
	return "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes."
}
func (*Edit) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}
func (*Edit) PromptGuidelines() []string {
	return []string{
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	}
}
func (*Edit) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},
"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
 "items":{"type":"object","properties":{
  "oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},
  "newText":{"type":"string","description":"Replacement text for this targeted edit."}},
  "required":["oldText","newText"],"additionalProperties":false}}},
"required":["path","edits"],"additionalProperties":false}`)
}

// Replacement is one oldText -> newText pair.
type Replacement struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// parseEditArgs accepts the normal shape plus two common model slips: a
// single oldText/newText at top level, or edits as a JSON string.
func parseEditArgs(args json.RawMessage) (string, []Replacement, error) {
	var raw struct {
		Path    string          `json:"path"`
		Edits   json.RawMessage `json:"edits"`
		OldText *string         `json:"oldText"`
		NewText *string         `json:"newText"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return "", nil, err
	}
	var edits []Replacement
	if len(raw.Edits) > 0 {
		var s string
		if json.Unmarshal(raw.Edits, &s) == nil {
			raw.Edits = json.RawMessage(s)
		}
		var one Replacement
		if err := json.Unmarshal(raw.Edits, &edits); err != nil {
			if json.Unmarshal(raw.Edits, &one) == nil && (one.OldText != "" || one.NewText != "") {
				edits = []Replacement{one}
			}
		}
	}
	if raw.OldText != nil && raw.NewText != nil {
		edits = append(edits, Replacement{OldText: *raw.OldText, NewText: *raw.NewText})
	}
	if raw.Path == "" || len(edits) == 0 {
		return "", nil, fmt.Errorf("edit needs a path and at least one replacement")
	}
	return raw.Path, edits, nil
}

// ApplyEdits performs all replacements against the original content and
// returns the new content. Every oldText must occur exactly once.
func ApplyEdits(content string, edits []Replacement, path string) (string, error) {
	type span struct {
		start, end int
		repl       string
	}
	var spans []span
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Errorf("edit %d: oldText is empty", i+1)
		}
		first := strings.Index(content, e.OldText)
		if first < 0 {
			return "", fmt.Errorf("Could not find text to replace in %s (edit %d). Read the file to see its exact current content.", path, i+1)
		}
		if strings.Index(content[first+1:], e.OldText) >= 0 {
			return "", fmt.Errorf("Text for edit %d appears more than once in %s. Include more context so it is unique.", i+1, path)
		}
		spans = append(spans, span{first, first + len(e.OldText), e.NewText})
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", fmt.Errorf("edits overlap in %s; merge them into one edit", path)
		}
	}
	var sb strings.Builder
	pos := 0
	for _, s := range spans {
		sb.WriteString(content[pos:s.start])
		sb.WriteString(s.repl)
		pos = s.end
	}
	sb.WriteString(content[pos:])
	return sb.String(), nil
}

func (e *Edit) Execute(ctx context.Context, _ string, args json.RawMessage, _ func(Result)) Result {
	path, edits, err := parseEditArgs(args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	abs := Resolve(path, e.cwd)
	data, err := os.ReadFile(abs)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Could not edit file: %s. %v", path, err))
	}
	raw := string(data)
	bom := ""
	if strings.HasPrefix(raw, "\uFEFF") {
		bom, raw = "\uFEFF", raw[3:]
	}
	crlf := strings.Contains(raw, "\r\n")
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	edited, err := ApplyEdits(norm, edits, path)
	if err != nil {
		return ErrorResult(err.Error())
	}
	final := edited
	if crlf {
		final = strings.ReplaceAll(edited, "\n", "\r\n")
	}
	if err := writeFileAtomic(abs, []byte(bom+final), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("Could not write file: %v", err))
	}
	res := TextResult(fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), path))
	res.Details = map[string]any{"diff": Diff(norm, edited)}
	return res
}

// Diff renders a compact line diff: "-" removed, "+" added, with a little context.
func Diff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	// Longest common subsequence over lines, fine for tool-sized files.
	n, m := len(al), len(bl)
	if n*m > 4_000_000 {
		return "(diff too large to show)"
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	type line struct {
		tag  byte
		text string
	}
	var ops []line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			ops = append(ops, line{' ', al[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, line{'-', al[i]})
			i++
		default:
			ops = append(ops, line{'+', bl[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, line{'-', al[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, line{'+', bl[j]})
	}
	// Keep only changed lines plus two lines of context around them.
	keep := make([]bool, len(ops))
	for k, op := range ops {
		if op.tag != ' ' {
			for c := k - 2; c <= k+2; c++ {
				if c >= 0 && c < len(ops) {
					keep[c] = true
				}
			}
		}
	}
	var sb strings.Builder
	last := -1
	for k, op := range ops {
		if !keep[k] {
			continue
		}
		if last >= 0 && k != last+1 {
			sb.WriteString("...\n")
		}
		sb.WriteByte(op.tag)
		sb.WriteByte(' ')
		sb.WriteString(op.text)
		sb.WriteByte('\n')
		last = k
	}
	return strings.TrimRight(sb.String(), "\n")
}
