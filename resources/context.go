package resources

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

// LoadContextFiles finds ~/.pig/AGENTS.md, then AGENTS.md and CLAUDE.md in
// each folder from the root down to cwd. AGENTS.override.md in a folder
// replaces both of the others there.
func LoadContextFiles(cwd string) []ContextFile {
	var out []ContextFile
	add := func(p string) {
		if data, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(data)) != "" {
			out = append(out, ContextFile{Path: p, Content: string(data)})
		}
	}
	add(filepath.Join(GlobalDir(), "AGENTS.md"))
	var chain []string
	for d := cwd; ; d = filepath.Dir(d) {
		chain = append([]string{d}, chain...)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range chain {
		if isFile(filepath.Join(d, "AGENTS.override.md")) {
			add(filepath.Join(d, "AGENTS.override.md"))
			continue
		}
		add(filepath.Join(d, "AGENTS.md"))
		add(filepath.Join(d, "CLAUDE.md"))
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
