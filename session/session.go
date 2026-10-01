// Package session stores a conversation as a JSONL file. Each line is an
// entry with an id and a parentId, so the file is a tree: you can go back
// to an earlier point and continue from there without losing anything.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tobyjackson/pig/ai"
)

// Version of the file format.
const Version = 1

// Header is the first line of a session file.
type Header struct {
	Type          string `json:"type"` // always "session"
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	Cwd           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
}

// Entry is one node in the tree. Type decides which fields are used:
// message, model_change, thinking_level_change, compaction, session_info,
// label, custom.
type Entry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`

	Message *ai.Message `json:"message,omitempty"`

	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	Summary          string    `json:"summary,omitempty"`
	FirstKeptEntryID string    `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int       `json:"tokensBefore,omitempty"`
	Usage            *ai.Usage `json:"usage,omitempty"`

	Name       string          `json:"name,omitempty"`
	TargetID   string          `json:"targetId,omitempty"`
	Label      string          `json:"label,omitempty"`
	CustomType string          `json:"customType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// Session is an open session file plus its in-memory tree. The store is
// written from the agent's goroutine (the OnMessage hook) and read from the
// TUI's, so every field is guarded by mu.
type Session struct {
	Header Header
	path   string // empty means in-memory only

	mu      sync.RWMutex
	entries []Entry
	byID    map[string]*Entry
	leafID  string
}

// Entries returns a copy of every entry in the file, in write order.
func (s *Session) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Entry(nil), s.entries...)
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// DirFor returns the folder that holds sessions for a working directory.
func DirFor(sessionsRoot, cwd string) string {
	esc := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimLeft(cwd, "/\\"))
	return filepath.Join(sessionsRoot, "--"+esc+"--")
}

// New starts a fresh session. If sessionsRoot is empty the session is
// never written to disk.
func New(sessionsRoot, cwd string) *Session {
	s := &Session{
		Header: Header{Type: "session", Version: Version, ID: newUUID(), Timestamp: nowStamp(), Cwd: cwd},
		byID:   map[string]*Entry{},
	}
	if sessionsRoot != "" {
		name := time.Now().UTC().Format("2006-01-02T15-04-05.000Z") + "_" + s.Header.ID + ".jsonl"
		s.path = filepath.Join(DirFor(sessionsRoot, cwd), name)
	}
	return s
}

// Open loads an existing session file.
func Open(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{path: path, byID: map[string]*Entry{}}
	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 256*1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if err := json.Unmarshal([]byte(line), &s.Header); err != nil || s.Header.Type != "session" {
				return nil, fmt.Errorf("%s: not a pig session file", path)
			}
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s: bad line: %w", path, err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Nothing else has the session yet, so no lock is needed.
	s.entries = entries
	for i := range s.entries {
		s.byID[s.entries[i].ID] = &s.entries[i]
	}
	if n := len(s.entries); n > 0 {
		s.leafID = s.entries[n-1].ID
	}
	return s, nil
}

// Path is the file on disk, or "" for an in-memory session.
func (s *Session) Path() string { return s.path }

// ID is the session's UUID.
func (s *Session) ID() string { return s.Header.ID }

// LeafID is the entry the next entry will hang from.
func (s *Session) LeafID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leafID
}

func (s *Session) append(e Entry) string {
	s.mu.Lock()
	e.ID = newID()
	for s.byID[e.ID] != nil {
		e.ID = newID()
	}
	e.Timestamp = nowStamp()
	if s.leafID != "" {
		leaf := s.leafID
		e.ParentID = &leaf
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID] = &s.entries[len(s.entries)-1]
	s.leafID = e.ID
	s.writeLine(e)
	s.mu.Unlock()
	return e.ID
}

func (s *Session) writeLine(v any) {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	fresh := false
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		fresh = true
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if fresh {
		h, _ := json.Marshal(s.Header)
		f.Write(append(h, '\n'))
	}
	line, _ := json.Marshal(v)
	f.Write(append(line, '\n'))
}

// AppendMessage records a message and returns the new entry id.
func (s *Session) AppendMessage(m ai.Message) string {
	msg := m
	return s.append(Entry{Type: "message", Message: &msg})
}

// AppendModelChange records a model switch.
func (s *Session) AppendModelChange(provider, modelID string) string {
	return s.append(Entry{Type: "model_change", Provider: provider, ModelID: modelID})
}

// AppendThinkingLevel records a thinking level switch.
func (s *Session) AppendThinkingLevel(level string) string {
	return s.append(Entry{Type: "thinking_level_change", ThinkingLevel: level})
}

// AppendCompaction records a summary that replaces everything before firstKept.
func (s *Session) AppendCompaction(summary, firstKept string, tokensBefore int, usage *ai.Usage) string {
	return s.append(Entry{Type: "compaction", Summary: summary, FirstKeptEntryID: firstKept, TokensBefore: tokensBefore, Usage: usage})
}

// AppendName records a display name for the session.
func (s *Session) AppendName(name string) string {
	return s.append(Entry{Type: "session_info", Name: name})
}

// AppendCustom records extension data that is never sent to the model.
func (s *Session) AppendCustom(customType string, data json.RawMessage) string {
	return s.append(Entry{Type: "custom", CustomType: customType, Data: data})
}

// Branch moves the leaf to an earlier entry. New entries then form a new
// branch; nothing is deleted.
func (s *Session) Branch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return fmt.Errorf("no entry %s", id)
	}
	s.leafID = id
	return nil
}

// Path from root to the current leaf, oldest first.
func (s *Session) BranchEntries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.branchEntries()
}

// branchEntries is BranchEntries without the lock, for callers that already
// hold one.
func (s *Session) branchEntries() []Entry {
	var out []Entry
	id := s.leafID
	for id != "" {
		e, ok := s.byID[id]
		if !ok {
			break
		}
		out = append(out, *e)
		if e.ParentID == nil {
			break
		}
		id = *e.ParentID
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Name returns the latest display name on the current branch.
func (s *Session) Name() string {
	name := ""
	for _, e := range s.BranchEntries() {
		if e.Type == "session_info" && e.Name != "" {
			name = e.Name
		}
	}
	return name
}

// LatestModel returns the last model change on the branch, if any.
func (s *Session) LatestModel() (provider, modelID string, ok bool) {
	for _, e := range s.BranchEntries() {
		if e.Type == "model_change" {
			provider, modelID, ok = e.Provider, e.ModelID, true
		}
	}
	return
}

// LatestThinkingLevel returns the last thinking level change on the branch.
func (s *Session) LatestThinkingLevel() (string, bool) {
	level, ok := "", false
	for _, e := range s.BranchEntries() {
		if e.Type == "thinking_level_change" {
			level, ok = e.ThinkingLevel, true
		}
	}
	return level, ok
}

// Context is what the model should see: an optional summary of older
// history, then the kept messages.
type Context struct {
	Summary  string
	Messages []ai.Message
	// Entry ids of the kept messages, parallel to Messages.
	EntryIDs []string
}

// BuildContext walks the branch and honours the newest compaction.
func (s *Session) BuildContext() Context {
	entries := s.BranchEntries()
	var ctx Context
	// Find the last compaction on the branch.
	compIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "compaction" {
			compIdx = i
			break
		}
	}
	start := 0
	if compIdx >= 0 {
		ctx.Summary = entries[compIdx].Summary
		start = compIdx + 1 // default: nothing before the compaction kept
		for i, e := range entries {
			if e.ID == entries[compIdx].FirstKeptEntryID {
				start = i
				break
			}
		}
	}
	for i := start; i < len(entries); i++ {
		e := entries[i]
		if e.Type == "message" && e.Message != nil {
			ctx.Messages = append(ctx.Messages, *e.Message)
			ctx.EntryIDs = append(ctx.EntryIDs, e.ID)
		}
	}
	return ctx
}

// Messages returns every message on the branch, ignoring compaction.
func (s *Session) Messages() []ai.Message {
	var out []ai.Message
	for _, e := range s.BranchEntries() {
		if e.Type == "message" && e.Message != nil {
			out = append(out, *e.Message)
		}
	}
	return out
}

// Clone copies the current branch into a brand-new session file. If upToID
// is set, entries after it are left out.
func (s *Session) Clone(sessionsRoot, upToID string) *Session {
	n := New(sessionsRoot, s.Header.Cwd)
	n.Header.ParentSession = s.path
	for _, e := range s.BranchEntries() {
		if e.Type == "message" || e.Type == "model_change" || e.Type == "thinking_level_change" || e.Type == "compaction" || e.Type == "session_info" {
			copy := e
			copy.ID, copy.ParentID = "", nil
			if copy.Type == "compaction" {
				// Ids change in the clone, so drop the kept marker: the
				// summary then stands for everything before it.
				copy.FirstKeptEntryID = ""
			}
			n.append(copy)
		}
		if e.ID == upToID {
			break
		}
	}
	return n
}

// Info is a summary line for session pickers.
type Info struct {
	Path      string
	ID        string
	Name      string
	Cwd       string
	Modified  time.Time
	Messages  int
	FirstUser string
}

// List returns sessions for a working directory, newest first.
func List(sessionsRoot, cwd string) []Info {
	dir := DirFor(sessionsRoot, cwd)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var out []Info
	for _, f := range files {
		s, err := Open(f)
		if err != nil {
			continue
		}
		st, _ := os.Stat(f)
		info := Info{Path: f, ID: s.ID(), Name: s.Name(), Cwd: s.Header.Cwd}
		if st != nil {
			info.Modified = st.ModTime()
		}
		for _, m := range s.Messages() {
			if m.Role == "user" || m.Role == "assistant" {
				info.Messages++
			}
			if m.Role == "user" && info.FirstUser == "" {
				info.FirstUser = firstLine(m.TextContent())
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// FindByID locates a session file by full or partial UUID.
func FindByID(sessionsRoot, cwd, partial string) (string, bool) {
	for _, info := range List(sessionsRoot, cwd) {
		if strings.HasPrefix(info.ID, partial) {
			return info.Path, true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(sessionsRoot, "*", "*"+partial+"*.jsonl"))
	if len(matches) > 0 {
		return matches[0], true
	}
	return "", false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
