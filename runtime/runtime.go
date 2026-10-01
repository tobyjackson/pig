// Package runtime is the SDK: it wires the model, tools, session file,
// skills, extensions and compaction into one Session you can prompt.
package runtime

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/tobyjackson/pig/agent"
	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/extensions"
	"github.com/tobyjackson/pig/resources"
	"github.com/tobyjackson/pig/session"
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
