package runtime

import (
	"context"
	"strings"

	"github.com/tobyjackson/pig/extensions"
	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/tools"
)

// loadResources discovers skills, prompt templates, context files and
// extensions, then rebuilds the tool list and the system prompt from them.
func (s *Session) loadResources() {
	cwd := s.Opts.Cwd
	// Resource files that are malformed are reported rather than silently
	// skipped, so a template or skill that never shows up can be explained.
	warn := func(what, msg string) {
		s.emit(Event{Type: "notify", Text: what + ": " + msg, Level: "warning"})
	}
	if !s.Opts.NoSkills {
		s.Skills = resources.LoadSkills(cwd, s.Trusted, append(s.Settings.Skills, s.Opts.SkillPaths...), func(msg string) {
			warn("skill", msg)
		})
	} else {
		s.Skills = nil
	}
	if !s.Opts.NoPrompts {
		s.Prompts = resources.LoadPromptTemplates(cwd, s.Trusted, append(s.Settings.Prompts, s.Opts.PromptPaths...), func(msg string) {
			warn("prompt template", msg)
		})
	} else {
		s.Prompts = nil
	}
	if !s.Opts.NoContextFiles {
		s.ContextFiles = resources.LoadContextFiles(cwd)
	} else {
		s.ContextFiles = nil
	}
	if s.Exts != nil {
		s.Exts.Close()
		s.Exts = nil
	}
	if !s.Opts.NoExtensions {
		paths := extensions.Discover(cwd, s.Trusted, append(s.Settings.Extensions, s.Opts.ExtensionPaths...))
		s.Exts = extensions.Load(context.Background(), paths, cwd, func(n extensions.Notice) {
			s.emit(Event{Type: "notify", Text: n.Extension + ": " + n.Message, Level: n.Level})
		})
	}
	s.rebuildTools()
	s.rebuildSystemPrompt()
}

// rebuildTools builds the tool list: the four built-ins, filtered, with any
// extension tool of the same name replacing a built-in.
func (s *Session) rebuildTools() {
	var ts []tools.Tool
	if !s.Opts.NoTools {
		bash := tools.NewBash(s.Opts.Cwd, s.Settings.ShellPath)
		bash.Env = s.sessionEnv()
		ts = []tools.Tool{tools.NewRead(s.Opts.Cwd), bash, tools.NewEdit(s.Opts.Cwd), tools.NewWrite(s.Opts.Cwd)}
		ts = tools.Filter(ts, s.Opts.Tools, s.Opts.ExcludeTools)
	}
	if s.Exts != nil {
		// Extension tools may replace a built-in of the same name.
		for _, et := range s.Exts.Tools() {
			replaced := false
			for i, t := range ts {
				if t.Name() == et.Name() {
					ts[i] = et
					replaced = true
				}
			}
			if !replaced {
				ts = append(ts, et)
			}
		}
	}
	s.Agent.Tools = ts
}

// sessionEnv is the PIG_* environment every command sees.
func (s *Session) sessionEnv() map[string]string {
	env := map[string]string{
		"PIG_SESSION_ID": s.Store.ID(), "PIG_PROVIDER": s.Agent.Model.Provider,
		"PIG_MODEL": s.Agent.Model.ID, "PIG_REASONING_LEVEL": s.Agent.ThinkingLevel, "PIG_CODING_AGENT": "true",
	}
	if p := s.Store.Path(); p != "" {
		env["PIG_SESSION_FILE"] = p
	}
	return env
}

// rebuildSystemPrompt composes the prompt from SYSTEM.md/APPEND_SYSTEM.md,
// the flags, the tools, the context files and the skills.
func (s *Session) rebuildSystemPrompt() {
	replace, appendText := resources.SystemPromptOverride(s.Opts.Cwd, s.Trusted)
	custom := s.Opts.SystemPrompt
	if custom == "" {
		custom = replace
	}
	if s.Opts.AppendSystemPrompt != "" {
		appendText = strings.TrimSpace(appendText + "\n\n" + s.Opts.AppendSystemPrompt)
	}
	s.Agent.SystemPrompt = buildSystemPrompt(promptOptions{
		custom: custom, appendText: appendText, tools: s.Agent.Tools, cwd: s.Opts.Cwd,
		contextFiles: s.ContextFiles, skills: s.Skills,
	})
}
