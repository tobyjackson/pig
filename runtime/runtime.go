// Package runtime is the SDK: it wires the model, tools, session file,
// skills, extensions and compaction into one Session you can prompt.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tobyjackson/pig/agent"
	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/compaction"
	"github.com/tobyjackson/pig/extensions"
	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/session"
	"github.com/tobyjackson/pig/tools"
)

// Options configure a new Session. Zero values mean "use the default".
type Options struct {
	Cwd           string
	Model         string // "provider/id", an id, or a substring
	ThinkingLevel string
	Mode          string // tui | print | json | rpc

	// Session choice: at most one of these.
	SessionPath string // resume this file (or partial id)
	Continue    bool   // resume the most recent session for cwd
	ForkPath    string // copy this session into a new one
	Ephemeral   bool   // no session file

	Tools        []string // allow-list of tool names
	ExcludeTools []string
	NoTools      bool

	SystemPrompt                                      string
	AppendSystemPrompt                                string
	NoSkills, NoPrompts, NoExtensions, NoContextFiles bool
	SkillPaths, PromptPaths, ExtensionPaths           []string

	// Trust for project-local files: "yes", "no", or "" to decide from
	// saved decisions, then TrustPrompt, then settings.defaultProjectTrust.
	Trust       string
	TrustPrompt func(cwd string) (trusted, remember bool)

	// Stream replaces the network call (tests, custom providers).
	Stream ai.StreamFunc
}

// Event is what Subscribe delivers. Agent events pass through unchanged;
// the runtime adds: compaction_start, compaction_end, notify, model_change,
// thinking_level_change, session_switch.
type Event = agent.Event

// Command is something the user can type as /name.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"` // skill | prompt | extension
	Path        string `json:"path,omitempty"`
}

// Session is one live conversation.
type Session struct {
	Opts     Options
	Settings resources.Settings
	Registry *ai.Registry
	Agent    *agent.Agent
	Store    *session.Session
	Trusted  bool

	Skills       []resources.Skill
	Prompts      []resources.PromptTemplate
	ContextFiles []resources.ContextFile
	Exts         *extensions.Manager

	mu             sync.Mutex
	autoCompaction bool
	compacting     bool
	listeners      []func(Event)
	pending        []Event
	sessionsRoot   string
}

// New builds a session: loads settings, picks a model, opens or creates
// the session file, and discovers resources.
func New(opts Options) (*Session, error) {
	if opts.Cwd == "" {
		opts.Cwd, _ = os.Getwd()
	}
	opts.Cwd, _ = filepath.Abs(opts.Cwd)
	s := &Session{Opts: opts}

	s.Trusted = s.decideTrust()
	s.Settings = resources.LoadSettings(opts.Cwd, s.Trusted)
	s.sessionsRoot = resources.SessionsDir(s.Settings)
	s.autoCompaction = s.Settings.CompactionEnabled()

	reg, err := ai.NewRegistry(resources.GlobalDir())
	if err != nil {
		return nil, err
	}
	s.Registry = reg

	if err := s.openStore(); err != nil {
		return nil, err
	}

	a := agent.New()
	if opts.Stream != nil {
		a.Stream = opts.Stream
	}
	a.APIKey = reg.APIKey
	a.SteeringMode = s.Settings.SteeringMode
	a.FollowUpMode = s.Settings.FollowUpMode
	s.Agent = a

	if err := s.pickModel(); err != nil {
		return nil, err
	}
	s.pickThinking()
	s.loadResources()
	s.installHooks()
	s.reloadAgentMessages()
	if s.Exts != nil {
		s.Exts.Emit("session_start", map[string]any{"reason": "startup", "sessionFile": s.Store.Path(), "cwd": opts.Cwd})
	}
	return s, nil
}

func (s *Session) decideTrust() bool {
	switch s.Opts.Trust {
	case "yes":
		return true
	case "no":
		return false
	}
	switch resources.TrustDecision(s.Opts.Cwd) {
	case "yes":
		return true
	case "no":
		return false
	}
	if !resources.HasProjectFiles(s.Opts.Cwd) {
		return false
	}
	def := resources.LoadSettings(s.Opts.Cwd, false).DefaultProjectTrust
	if s.Opts.TrustPrompt != nil && def == "ask" {
		trusted, remember := s.Opts.TrustPrompt(s.Opts.Cwd)
		if remember {
			_ = resources.SaveTrust(s.Opts.Cwd, trusted)
		}
		return trusted
	}
	return def == "always"
}

func (s *Session) openStore() error {
	root := s.sessionsRoot
	if s.Opts.Ephemeral {
		root = ""
	}
	switch {
	case s.Opts.SessionPath != "":
		path := s.resolveSessionPath(s.Opts.SessionPath)
		st, err := session.Open(path)
		if err != nil {
			return err
		}
		s.Store = st
	case s.Opts.ForkPath != "":
		path := s.resolveSessionPath(s.Opts.ForkPath)
		src, err := session.Open(path)
		if err != nil {
			return err
		}
		s.Store = src.Clone(root, "")
	case s.Opts.Continue:
		list := session.List(s.sessionsRoot, s.Opts.Cwd)
		if len(list) == 0 {
			s.Store = session.New(root, s.Opts.Cwd)
		} else {
			st, err := session.Open(list[0].Path)
			if err != nil {
				return err
			}
			s.Store = st
		}
	default:
		s.Store = session.New(root, s.Opts.Cwd)
	}
	return nil
}

func (s *Session) resolveSessionPath(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if found, ok := session.FindByID(s.sessionsRoot, s.Opts.Cwd, p); ok {
		return found
	}
	return p
}

func (s *Session) pickModel() error {
	reg := s.Registry
	var m ai.Model
	var ok bool
	if s.Opts.Model != "" {
		m, ok = reg.Find(s.Opts.Model)
		if !ok {
			return fmt.Errorf("no model matches %q (try --list-models)", s.Opts.Model)
		}
	} else if p, id, found := s.Store.LatestModel(); found {
		m, ok = reg.Find(p + "/" + id)
	}
	if !ok && s.Settings.DefaultModel != "" {
		key := s.Settings.DefaultModel
		if s.Settings.DefaultProvider != "" {
			key = s.Settings.DefaultProvider + "/" + key
		}
		m, ok = reg.Find(key)
	}
	if !ok {
		m, ok = reg.Default()
	}
	if !ok {
		return errors.New("no model available: set ANTHROPIC_API_KEY, OPENAI_API_KEY or OPENROUTER_API_KEY, add a key with `pig login <provider>`, or configure ~/.pig/models.json")
	}
	if s.Opts.Stream == nil && !reg.HasAuth(m.Provider) {
		return fmt.Errorf("no API key for %s (model %s): run `pig login %s` or set the provider's environment variable", m.Provider, m.Key(), m.Provider)
	}
	s.Agent.Model = m
	return nil
}

func (s *Session) pickThinking() {
	level := s.Opts.ThinkingLevel
	if level == "" {
		if l, ok := s.Store.LatestThinkingLevel(); ok {
			level = l
		}
	}
	if level == "" {
		level = s.Settings.DefaultThinkingLevel
	}
	if level == "" {
		level = "high"
	}
	if !ai.ValidThinkingLevel(level) {
		level = "high"
	}
	s.Agent.ThinkingLevel = level
}

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

func (s *Session) installHooks() {
	a := s.Agent
	a.Subscribe(func(e Event) { s.emit(e) })
	// A queued message is filtered when the agent consumes it, not when it is
	// queued, so an extension can still see steers and follow-ups without
	// blocking the caller that sent them.
	a.Hooks.FilterQueued = func(m ai.Message) (ai.Message, bool) {
		if ext := s.applyInputHooks(m, true); ext.Role != "" {
			return ext, true
		}
		return m, false
	}
	a.Hooks.OnMessage = func(m ai.Message) { s.Store.AppendMessage(m) }
	a.Hooks.BeforeTurn = func(ctx context.Context) error {
		if !s.autoCompaction {
			return nil
		}
		msgs := a.Messages()
		if compaction.ShouldCompact(compaction.ContextTokens(a.SystemPrompt, msgs), a.Model.ContextWindow, s.reserveTokens()) {
			if _, err := s.compact(ctx, "", "threshold"); err != nil && !errors.Is(err, errNothingToCompact) {
				s.emit(Event{Type: "notify", Text: "compaction failed: " + err.Error(), Level: "warning"})
			}
		}
		return nil
	}
	a.Hooks.ToolCall = func(name, callID string, args json.RawMessage) (json.RawMessage, string) {
		if s.Exts == nil {
			return nil, ""
		}
		var input any
		_ = json.Unmarshal(args, &input)
		reply := s.Exts.Emit("tool_call", map[string]any{"toolName": name, "toolCallId": callID, "input": input})
		if b, ok := reply["block"]; ok && string(b) == "true" {
			reason := "blocked by extension"
			if r, ok := reply["reason"]; ok {
				var rs string
				if json.Unmarshal(r, &rs) == nil && rs != "" {
					reason = rs
				}
			}
			return nil, reason
		}
		if in, ok := reply["input"]; ok && len(in) > 0 {
			return in, ""
		}
		return nil, ""
	}
	a.Hooks.ToolResult = func(name, callID string, args json.RawMessage, r tools.Result) tools.Result {
		if s.Exts == nil {
			return r
		}
		var input any
		_ = json.Unmarshal(args, &input)
		reply := s.Exts.Emit("tool_result", map[string]any{"toolName": name, "toolCallId": callID, "input": input, "content": r.Content, "isError": r.IsError})
		if c, ok := reply["content"]; ok {
			var content []ai.Content
			if json.Unmarshal(c, &content) == nil && len(content) > 0 {
				r.Content = content
			}
		}
		if e, ok := reply["isError"]; ok {
			r.IsError = string(e) == "true"
		}
		return r
	}
}

// reloadAgentMessages sets the agent's history from the session branch,
// honouring compaction.
func (s *Session) reloadAgentMessages() {
	ctx := s.Store.BuildContext()
	var msgs []ai.Message
	if ctx.Summary != "" {
		msgs = append(msgs, compaction.SummaryMessage(ctx.Summary))
	}
	msgs = append(msgs, ctx.Messages...)
	s.Agent.SetMessages(msgs)
}

// Subscribe adds an event listener and returns a function that removes it.
// Warnings raised while the session was being built are delivered immediately,
// since they would otherwise be lost before the listener existed.
func (s *Session) Subscribe(fn func(Event)) func() {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.listeners = append(s.listeners, fn)
	idx := len(s.listeners) - 1
	s.mu.Unlock()
	for _, e := range pending {
		fn(e)
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if idx < len(s.listeners) {
			s.listeners[idx] = func(Event) {}
		}
	}
}

func (s *Session) emit(e Event) {
	s.mu.Lock()
	ls := append([]func(Event){}, s.listeners...)
	if len(ls) == 0 {
		// No listener yet, which means the session is still being built. Hold
		// the event so Subscribe can deliver it rather than dropping it.
		if len(s.pending) < 64 {
			s.pending = append(s.pending, e)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	for _, l := range ls {
		l(e)
	}
}

// Prompt sends text as the user and waits until the agent is idle. If the
// agent is busy the text is queued as a steering message.
func (s *Session) Prompt(ctx context.Context, text string) error {
	return s.PromptMessage(ctx, ai.UserMessage(text))
}

// PromptMessage is Prompt for a message that may carry images.
func (s *Session) PromptMessage(ctx context.Context, m ai.Message) error {
	if s.Agent.IsRunning() {
		s.SteerMessage(m)
		return nil
	}
	if ext := s.applyInputHooks(m, false); ext.Role != "" {
		m = ext
	} else {
		// An extension blocked the prompt.
		return nil
	}
	if s.Exts != nil {
		reply := s.Exts.Emit("before_agent_start", map[string]any{"prompt": m.TextContent(), "systemPrompt": s.Agent.SystemPrompt})
		if sp, ok := reply["systemPrompt"]; ok {
			var v string
			if json.Unmarshal(sp, &v) == nil && v != "" {
				s.Agent.SystemPrompt = v
			}
		}
	}
	err := s.Agent.Prompt(ctx, m)
	if err != nil && isOverflow(err) && s.autoCompaction && ctx.Err() == nil {
		if _, cerr := s.compact(ctx, "", "overflow"); cerr == nil {
			err = s.Agent.Continue(ctx)
		}
	}
	if s.Exts != nil {
		s.Exts.Emit("agent_end", map[string]any{})
	}
	return err
}

func isOverflow(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "prompt is too long") || strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "context window") || strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "maximum context length")
}

// Steer queues text for delivery after the current tool calls.
func (s *Session) Steer(text string) { s.SteerMessage(ai.UserMessage(text)) }

// FollowUp queues text for delivery once the agent is idle.
func (s *Session) FollowUp(text string) { s.FollowUpMessage(ai.UserMessage(text)) }

// SteerMessage and FollowUpMessage queue a message for the agent. The input
// hooks run later, in the agent's goroutine, via Hooks.FilterQueued: a message
// that arrives while the agent is busy used to skip the hooks entirely, and
// running them here would block the caller, which for the TUI is the UI thread.
func (s *Session) SteerMessage(m ai.Message)    { s.Agent.Steer(m) }
func (s *Session) FollowUpMessage(m ai.Message) { s.Agent.FollowUp(m) }

// applyInputHooks lets extensions see and rewrite a user message. A reply with
// "block": true drops the message, which is reported as an empty Role so
// callers can tell "blocked" from "handled".
func (s *Session) applyInputHooks(m ai.Message, queued bool) ai.Message {
	if s.Exts == nil {
		return m
	}
	reply := s.Exts.Emit("input", map[string]any{"text": m.TextContent(), "queued": queued})
	if b, ok := reply["block"]; ok {
		var blocked bool
		if json.Unmarshal(b, &blocked) == nil && blocked {
			reason := "blocked by extension"
			if r, ok := reply["reason"]; ok {
				var rs string
				if json.Unmarshal(r, &rs) == nil && rs != "" {
					reason = rs
				}
			}
			s.emit(Event{Type: "notify", Text: reason, Level: "warning"})
			return ai.Message{}
		}
	}
	if t, ok := reply["text"]; ok {
		var v string
		if json.Unmarshal(t, &v) == nil && v != m.TextContent() {
			// Keep any non-text content, such as images, and replace the text.
			out := ai.UserMessage(v)
			for _, c := range m.Content {
				if c.Type != "text" {
					out.Content = append(out.Content, c)
				}
			}
			return out
		}
	}
	return m
}

// Abort stops the current run.
func (s *Session) Abort() { s.Agent.Abort() }

// IsRunning reports whether a prompt is in progress.
func (s *Session) IsRunning() bool { return s.Agent.IsRunning() }

// Model returns the active model.
func (s *Session) Model() ai.Model { return s.Agent.Model }

// ThinkingLevel returns the active thinking level.
func (s *Session) ThinkingLevel() string { return s.Agent.ThinkingLevel }

// SetModel switches models and records the change.
func (s *Session) SetModel(m ai.Model) {
	if m.Provider == s.Agent.Model.Provider && m.ID == s.Agent.Model.ID {
		return
	}
	s.Agent.Model = m
	s.Store.AppendModelChange(m.Provider, m.ID)
	s.rebuildTools()
	s.emit(Event{Type: "model_change", Text: m.Key()})
}

// SetThinkingLevel changes the level and records it.
func (s *Session) SetThinkingLevel(level string) error {
	if !ai.ValidThinkingLevel(level) {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	if level == s.Agent.ThinkingLevel {
		return nil
	}
	s.Agent.ThinkingLevel = level
	s.Store.AppendThinkingLevel(level)
	s.rebuildTools()
	s.emit(Event{Type: "thinking_level_change", Text: level})
	return nil
}

// CycleModel moves to the next available model.
func (s *Session) CycleModel(backwards bool) ai.Model {
	avail := s.Registry.Available()
	if len(avail) == 0 {
		return s.Agent.Model
	}
	idx := 0
	for i, m := range avail {
		if m.Provider == s.Agent.Model.Provider && m.ID == s.Agent.Model.ID {
			idx = i
		}
	}
	if backwards {
		idx = (idx - 1 + len(avail)) % len(avail)
	} else {
		idx = (idx + 1) % len(avail)
	}
	s.SetModel(avail[idx])
	return avail[idx]
}

// CycleThinkingLevel moves to the next level, wrapping around.
func (s *Session) CycleThinkingLevel() string {
	levels := ai.ThinkingLevels
	if !s.Agent.Model.Reasoning {
		return s.Agent.ThinkingLevel
	}
	idx := 0
	for i, l := range levels {
		if l == s.Agent.ThinkingLevel {
			idx = i
		}
	}
	next := levels[(idx+1)%len(levels)]
	_ = s.SetThinkingLevel(next)
	return next
}

// Messages returns the agent's current history (post-compaction view).
func (s *Session) Messages() []ai.Message { return s.Agent.Messages() }

// AutoCompaction reports whether automatic compaction is on.
func (s *Session) AutoCompaction() bool { return s.autoCompaction }

// SetAutoCompaction turns automatic compaction on or off.
func (s *Session) SetAutoCompaction(on bool) { s.autoCompaction = on }

// Compact summarises older history now. custom adds focus instructions.
func (s *Session) Compact(ctx context.Context, custom string) (string, error) {
	return s.compact(ctx, custom, "manual")
}

var errNothingToCompact = errors.New("nothing to compact yet")

// reserveTokens is the configured reserve, shrunk for small context windows
// so the threshold can never be negative.
func (s *Session) reserveTokens() int {
	r := s.Settings.Compaction.ReserveTokens
	if w := s.Agent.Model.ContextWindow; w > 0 && r > w/2 {
		r = w / 4
	}
	return r
}

// compactionBudget is the largest kept part compaction may leave behind. The
// next request adds the system prompt, the tools and the reply, so the kept part
// and the reserve have to fit under the window together. A floor keeps the
// figure sane when a model reports a tiny or missing context window.
func (s *Session) compactionBudget() int {
	w := s.Agent.Model.ContextWindow
	if w <= 0 {
		return s.Settings.Compaction.KeepRecentTokens
	}
	b := w - s.reserveTokens() - len(s.Agent.SystemPrompt)/4
	if b < s.Settings.Compaction.KeepRecentTokens {
		// KeepRecentTokens is the user's stated preference; never override it
		// downward, or compaction would keep less than asked for.
		return s.Settings.Compaction.KeepRecentTokens
	}
	return b
}

func (s *Session) compact(ctx context.Context, custom, reason string) (string, error) {
	s.mu.Lock()
	if s.compacting {
		s.mu.Unlock()
		return "", errors.New("compaction already running")
	}
	s.compacting = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.compacting = false; s.mu.Unlock() }()

	bctx := s.Store.BuildContext()
	msgs := bctx.Messages
	cut := compaction.CutIndex(msgs, s.Settings.Compaction.KeepRecentTokens, s.compactionBudget())
	if cut <= 0 || cut >= len(msgs) {
		return "", errNothingToCompact
	}
	s.emit(Event{Type: "compaction_start", Reason: reason})
	toSummarize := msgs[:cut]
	kept := msgs[cut:]
	before := compaction.ContextTokens(s.Agent.SystemPrompt, s.Agent.Messages())
	summary, usage, err := compaction.Summarize(ctx, s.Agent.Stream, s.Agent.Model, s.Registry.APIKey(s.Agent.Model.Provider), toSummarize, bctx.Summary, custom)
	if err != nil {
		s.emit(Event{Type: "compaction_end", Reason: reason, Error: err.Error()})
		return "", err
	}
	firstKept := ""
	if cut < len(bctx.EntryIDs) {
		firstKept = bctx.EntryIDs[cut]
	}
	s.Store.AppendCompaction(summary, firstKept, before, usage)
	var newMsgs []ai.Message
	newMsgs = append(newMsgs, compaction.SummaryMessage(summary))
	newMsgs = append(newMsgs, kept...)
	s.Agent.SetMessages(newMsgs)
	s.emit(Event{Type: "compaction_end", Reason: reason, Summary: summary})
	return summary, nil
}

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

// SetName gives the session a display name.
func (s *Session) SetName(name string) { s.Store.AppendName(name) }

// NewSession starts a fresh session in the same folder.
func (s *Session) NewSession() {
	root := s.sessionsRoot
	if s.Opts.Ephemeral {
		root = ""
	}
	s.Store = session.New(root, s.Opts.Cwd)
	s.Agent.SetMessages(nil)
	s.Agent.ClearQueue()
	s.rebuildTools()
	s.emit(Event{Type: "session_switch", Reason: "new", Text: s.Store.Path()})
}

// SwitchSession loads another session file.
func (s *Session) SwitchSession(path string) error {
	st, err := session.Open(s.resolveSessionPath(path))
	if err != nil {
		return err
	}
	s.Store = st
	if p, id, ok := st.LatestModel(); ok {
		if m, found := s.Registry.Find(p + "/" + id); found {
			s.Agent.Model = m
		}
	}
	if l, ok := st.LatestThinkingLevel(); ok && ai.ValidThinkingLevel(l) {
		s.Agent.ThinkingLevel = l
	}
	s.reloadAgentMessages()
	s.rebuildTools()
	s.emit(Event{Type: "session_switch", Reason: "resume", Text: s.Store.Path()})
	return nil
}

// ForkPoint is a user message you can fork from.
type ForkPoint struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
}

// ForkPoints lists user messages on the current branch.
func (s *Session) ForkPoints() []ForkPoint {
	var out []ForkPoint
	for _, e := range s.Store.BranchEntries() {
		if e.Type == "message" && e.Message != nil && e.Message.Role == "user" {
			out = append(out, ForkPoint{EntryID: e.ID, Text: e.Message.TextContent()})
		}
	}
	return out
}

// Fork copies the branch up to (not including) the given user message into
// a new session and returns that message's text for editing.
func (s *Session) Fork(entryID string) (string, error) {
	entries := s.Store.BranchEntries()
	upTo := ""
	text := ""
	for i, e := range entries {
		if e.ID == entryID {
			if e.Message != nil {
				text = e.Message.TextContent()
			}
			if i > 0 {
				upTo = entries[i-1].ID
			}
			break
		}
	}
	if text == "" && upTo == "" {
		return "", fmt.Errorf("no user message with id %s", entryID)
	}
	root := s.sessionsRoot
	if s.Opts.Ephemeral {
		root = ""
	}
	if upTo == "" {
		s.Store = session.New(root, s.Opts.Cwd)
	} else {
		s.Store = s.Store.Clone(root, upTo)
	}
	s.reloadAgentMessages()
	s.rebuildTools()
	s.emit(Event{Type: "session_switch", Reason: "fork", Text: s.Store.Path()})
	return text, nil
}

// Clone copies the whole current branch into a new session file.
func (s *Session) Clone() string {
	root := s.sessionsRoot
	if s.Opts.Ephemeral {
		root = ""
	}
	s.Store = s.Store.Clone(root, "")
	s.emit(Event{Type: "session_switch", Reason: "clone", Text: s.Store.Path()})
	return s.Store.Path()
}

// NavigateTree moves the active leaf to an earlier entry.
func (s *Session) NavigateTree(entryID string) error {
	if err := s.Store.Branch(entryID); err != nil {
		return err
	}
	s.reloadAgentMessages()
	return nil
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

// Reload re-reads skills, prompts, extensions and context files.
func (s *Session) Reload() {
	s.loadResources()
	if s.Exts != nil {
		s.Exts.Emit("session_start", map[string]any{"reason": "reload", "sessionFile": s.Store.Path(), "cwd": s.Opts.Cwd})
	}
}

// Close shuts down extensions.
func (s *Session) Close() {
	if s.Exts != nil {
		s.Exts.Close()
	}
}

// LastAssistantText returns the text of the newest assistant message.
func (s *Session) LastAssistantText() string {
	msgs := s.Agent.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i].TextContent()
		}
	}
	return ""
}

// Sessions lists saved sessions for this folder, newest first.
func (s *Session) Sessions() []session.Info { return session.List(s.sessionsRoot, s.Opts.Cwd) }

// SessionsRoot is the folder holding all session files.
func (s *Session) SessionsRoot() string { return s.sessionsRoot }

// SaveDefaultModel remembers the current model as the startup default.
func (s *Session) SaveDefaultModel() error {
	return resources.SaveGlobal(func(m map[string]any) {
		m["defaultProvider"] = s.Agent.Model.Provider
		m["defaultModel"] = s.Agent.Model.ID
		m["defaultThinkingLevel"] = s.Agent.ThinkingLevel
	})
}

// StartedAt is when the session file was created.
func (s *Session) StartedAt() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s.Store.Header.Timestamp)
	return t
}

// SaveTrustYes records that this project's .pig folder may be loaded.
func (s *Session) SaveTrustYes() error { return resources.SaveTrust(s.Opts.Cwd, true) }
