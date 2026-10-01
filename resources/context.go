package resources

// The context-file names and the first-match-wins rule are ported from pi:
// packages/coding-agent/src/core/resource-loader.ts (loadContextFileFromDir)
// Copyright (c) 2025 Mario Zechner, MIT licensed. See LICENSE.

import (
	"os"
	"path/filepath"
	"strings"
)

// ContextFile is an AGENTS.md style file whose text goes in the system prompt.
type ContextFile struct {
	Path    string
	Content string
}

// contextNames are tried in order in each folder. The first that exists wins,
// so a folder with both an AGENTS.md and a CLAUDE.md contributes one file, not
// two copies of the same instructions. An override replaces the rest.
var contextNames = []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"}

// loadContextFileFromDir returns the first context file present in dir, or a
// zero ContextFile when the folder has none.
func loadContextFileFromDir(dir string) ContextFile {
	for _, name := range contextNames {
		p := filepath.Join(dir, name)
		if !isFile(p) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return ContextFile{Path: p, Content: string(data)}
	}
	return ContextFile{}
}

// LoadContextFiles finds the global context file in ~/.pig, then the first
// context file in each folder from the root down to cwd. A folder's override
// file replaces the others in that folder.
func LoadContextFiles(cwd string) []ContextFile {
	var out []ContextFile
	add := func(f ContextFile) {
		if f.Path != "" && strings.TrimSpace(f.Content) != "" {
			out = append(out, f)
		}
	}
	add(loadContextFileFromDir(GlobalDir()))
	var chain []string
	for d := cwd; ; d = filepath.Dir(d) {
		chain = append([]string{d}, chain...)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range chain {
		add(loadContextFileFromDir(d))
	}
	return out
}

// SystemPromptOverride returns SYSTEM.md content if present (project first,
// then global) and APPEND_SYSTEM.md content likewise.
func SystemPromptOverride(cwd string, trusted bool) (replace, appendText string) {
	read := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	if trusted {
		replace = read(filepath.Join(ProjectDir(cwd), "SYSTEM.md"))
		appendText = read(filepath.Join(ProjectDir(cwd), "APPEND_SYSTEM.md"))
	}
	if replace == "" {
		replace = read(filepath.Join(GlobalDir(), "SYSTEM.md"))
	}
	if appendText == "" {
		appendText = read(filepath.Join(GlobalDir(), "APPEND_SYSTEM.md"))
	}
	return
}
