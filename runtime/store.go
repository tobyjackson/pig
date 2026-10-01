package runtime

import (
	"fmt"
	"os"
	"time"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/session"
)

// openStore opens or creates the session file according to Options.
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

// resolveSessionPath turns a partial id or a path into a session file path.
func (s *Session) resolveSessionPath(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if found, ok := session.FindByID(s.sessionsRoot, s.Opts.Cwd, p); ok {
		return found
	}
	return p
}

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

// SetName gives the session a display name.
func (s *Session) SetName(name string) { s.Store.AppendName(name) }

// Sessions lists saved sessions for this folder, newest first.
func (s *Session) Sessions() []session.Info { return session.List(s.sessionsRoot, s.Opts.Cwd) }

// SessionsRoot is the folder holding all session files.
func (s *Session) SessionsRoot() string { return s.sessionsRoot }

// StartedAt is when the session file was created.
func (s *Session) StartedAt() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s.Store.Header.Timestamp)
	return t
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

// Close shuts down extensions.
func (s *Session) Close() {
	if s.Exts != nil {
		s.Exts.Close()
	}
}

// Reload re-reads skills, prompts, extensions and context files.
func (s *Session) Reload() {
	s.loadResources()
	if s.Exts != nil {
		s.Exts.Emit("session_start", map[string]any{"reason": "reload", "sessionFile": s.Store.Path(), "cwd": s.Opts.Cwd})
	}
}
