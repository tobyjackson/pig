package runtime

// The system prompt's wording and section layout are ported from pi:
// packages/coding-agent/src/core/system-prompt.ts
// Copyright (c) 2025 Mario Zechner, MIT licensed. See LICENSE.

import (
	"strings"

	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/tools"
)

// promptOptions is everything the system prompt is built from.
type promptOptions struct {
	custom       string
	appendText   string
	tools        []tools.Tool
	cwd          string
	contextFiles []resources.ContextFile
	skills       []resources.Skill
}

// buildSystemPrompt mirrors pi's prompt: short, tool-aware, then project
// context, then the skills list, then the working directory.
func buildSystemPrompt(o promptOptions) string {
	names := tools.Names(o.tools)
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	readTool := ""
	if has("read") {
		readTool = "read"
	} else if has("bash") {
		readTool = "bash"
	}

	var sb strings.Builder
	if o.custom != "" {
		sb.WriteString(o.custom)
	} else {
		var snippets, guidelines []string
		seen := map[string]bool{}
		addG := func(g string) {
			g = strings.TrimSpace(g)
			if g != "" && !seen[g] {
				seen[g] = true
				guidelines = append(guidelines, g)
			}
		}
		for _, t := range o.tools {
			if p, ok := t.(tools.PromptInfo); ok {
				snippets = append(snippets, "- "+t.Name()+": "+p.PromptSnippet())
			}
		}
		if has("bash") {
			addG("Use bash for file operations like ls, rg, find")
		}
		for _, t := range o.tools {
			if p, ok := t.(tools.PromptInfo); ok {
				for _, g := range p.PromptGuidelines() {
					addG(g)
				}
			}
		}
		addG("Be concise in your responses")
		addG("Show file paths clearly when working with files")
		toolsList := "(none)"
		if len(snippets) > 0 {
			toolsList = strings.Join(snippets, "\n")
		}
		sb.WriteString("You are an expert programmer operating inside an agent harness. You are the custodian of this harness, and strive to improve how it works. You help users by understanding their input, reading files, executing commands, editing code, and writing new files.\n\n")
		sb.WriteString("Available tools:\n" + toolsList + "\n\n")
		sb.WriteString("In addition to the tools above, you may have access to other custom tools depending on the project.\n\n")
		sb.WriteString("Guidelines:\n")
		for _, g := range guidelines {
			sb.WriteString("- " + g + "\n")
		}
		sb.WriteString("\npig documentation (read only when the user asks about pig itself, its extensions, skills, prompt templates, or SDK): run `pig docs` to list topics and `pig docs <topic>` to read one.")
	}
	if o.appendText != "" {
		sb.WriteString("\n\n" + o.appendText)
	}
	if len(o.contextFiles) > 0 {
		sb.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
		for _, f := range o.contextFiles {
			sb.WriteString("<project_instructions path=\"" + f.Path + "\">\n" + f.Content + "\n</project_instructions>\n\n")
		}
		sb.WriteString("</project_context>\n")
	}
	if readTool != "" && len(o.skills) > 0 {
		sb.WriteString(resources.FormatSkillsForPrompt(o.skills, readTool))
	}
	sb.WriteString("\nCurrent working directory: " + strings.ReplaceAll(o.cwd, "\\", "/"))
	return sb.String()
}
