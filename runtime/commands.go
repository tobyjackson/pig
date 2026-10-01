package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/compaction"
	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/tools"
)

// Stats summarise the session so far.
type Stats struct {
	SessionFile    string   `json:"sessionFile"`
	SessionID      string   `json:"sessionId"`
	Name           string   `json:"sessionName,omitempty"`
	UserMessages   int      `json:"userMessages"`
	AssistantMsgs  int      `json:"assistantMessages"`
	ToolCalls      int      `json:"toolCalls"`
	Tokens         ai.Usage `json:"tokens"`
	ContextTokens  int      `json:"contextTokens"`
	ContextWindow  int      `json:"contextWindow"`
	ContextPercent float64  `json:"contextPercent"`
}

// Stats computes totals from the session branch.
func (s *Session) Stats() Stats {
	st := Stats{SessionFile: s.Store.Path(), SessionID: s.Store.ID(), Name: s.Store.Name(), ContextWindow: s.Agent.Model.ContextWindow}
	for _, m := range s.Store.Messages() {
		switch m.Role {
		case "user":
			st.UserMessages++
		case "assistant":
			st.AssistantMsgs++
			st.ToolCalls += len(m.ToolCalls())
			if m.Usage != nil {
				st.Tokens.Add(*m.Usage)
			}
		}
	}
	st.ContextTokens = compaction.ContextTokens(s.Agent.SystemPrompt, s.Agent.Messages())
	if st.ContextWindow > 0 {
		st.ContextPercent = float64(st.ContextTokens) / float64(st.ContextWindow) * 100
	}
	return st
}

// Commands lists skills, prompt templates and extension commands.
func (s *Session) Commands() []Command {
	var out []Command
	for _, sk := range s.Skills {
		out = append(out, Command{Name: "skill:" + sk.Name, Description: sk.Description, Source: "skill", Path: sk.Path})
	}
	for _, p := range s.Prompts {
		out = append(out, Command{Name: p.Name, Description: p.Description, Source: "prompt", Path: p.Path})
	}
	if s.Exts != nil {
		for _, c := range s.Exts.Commands() {
			out = append(out, Command{Name: c.Spec.Name, Description: c.Spec.Description, Source: "extension", Path: c.Ext.Path})
		}
	}
	return out
}

// ExpandInput turns a /command line into the text to send. handled is
// false when the input is not a known command. text may be empty when an
// extension handled the command without sending anything.
func (s *Session) ExpandInput(input string) (text string, handled bool, err error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return input, false, nil
	}
	name, args, _ := strings.Cut(trimmed[1:], " ")
	args = strings.TrimSpace(args)
	if strings.HasPrefix(name, "skill:") {
		want := strings.TrimPrefix(name, "skill:")
		for _, sk := range s.Skills {
			if sk.Name == want {
				body := resources.SkillBody(sk)
				out := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\n%s\n</skill>", sk.Name, sk.Path, strings.TrimSpace(body))
				if args != "" {
					out += "\n\n" + args
				}
				return out, true, nil
			}
		}
		return "", true, fmt.Errorf("unknown skill %q", want)
	}
	for _, p := range s.Prompts {
		if p.Name == name {
			return p.Expand(resources.SplitArgs(args)), true, nil
		}
	}
	if s.Exts != nil {
		res, ok, err := s.Exts.RunCommand(name, args)
		if ok {
			if err != nil {
				return "", true, err
			}
			if res.Notify != "" {
				s.emit(Event{Type: "notify", Text: res.Notify, Level: "info"})
			}
			return res.Message, true, nil
		}
	}
	return input, false, nil
}

// RunBash runs a user command (the "!" prefix) and returns its output.
func (s *Session) RunBash(ctx context.Context, command string, onData func([]byte)) (string, int, error) {
	b := tools.NewBash(s.Opts.Cwd, s.Settings.ShellPath)
	b.Env = s.sessionEnv()
	var sb strings.Builder
	code, err := b.Run(ctx, command, 0, func(p []byte) {
		sb.Write(p)
		if onData != nil {
			onData(p)
		}
	})
	return sb.String(), code, err
}

// RecordBash adds a user command and its output to the history so the
// model sees it on the next turn.
func (s *Session) RecordBash(command, output string, exitCode int) {
	t := tools.TruncateTail(output)
	text := fmt.Sprintf("I ran this command:\n```\n%s\n```\nOutput (exit code %d):\n```\n%s\n```", command, exitCode, t.Content)
	m := ai.UserMessage(text)
	s.Agent.SetMessages(append(s.Agent.Messages(), m))
	s.Store.AppendMessage(m)
}
