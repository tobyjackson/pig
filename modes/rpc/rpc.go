// Package rpc drives a session from another program: JSON commands come in
// on stdin, one per line; events and responses go out on stdout.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/modes/export"
	"github.com/tobyjackson/pig/runtime"
)

type command struct {
	Type               string `json:"type"`
	ID                 string `json:"id"`
	Message            string `json:"message"`
	StreamingBehavior  string `json:"streamingBehavior"`
	Provider           string `json:"provider"`
	ModelID            string `json:"modelId"`
	Level              string `json:"level"`
	Enabled            *bool  `json:"enabled"`
	CustomInstructions string `json:"customInstructions"`
	Command            string `json:"command"`
	SessionPath        string `json:"sessionPath"`
	EntryID            string `json:"entryId"`
	Name               string `json:"name"`
	OutputPath         string `json:"outputPath"`
	Mode               string `json:"mode"`
	Images             []struct {
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"images"`
}

// Run serves until stdin closes.
func Run(s *runtime.Session, in io.Reader, out io.Writer) error {
	var mu sync.Mutex
	write := func(v any) {
		data, _ := json.Marshal(v)
		mu.Lock()
		out.Write(append(data, '\n'))
		mu.Unlock()
	}
	s.Subscribe(func(e runtime.Event) { write(e) })
	respond := func(c command, ok bool, data any, err string) {
		r := map[string]any{"type": "response", "command": c.Type, "success": ok}
		if c.ID != "" {
			r["id"] = c.ID
		}
		if err != "" {
			r["error"] = err
		}
		if data != nil {
			r["data"] = data
		}
		write(r)
	}
	ctx := context.Background()
	var inflight sync.WaitGroup
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024*1024), 256*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var c command
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			respond(command{Type: "unknown"}, false, nil, "bad json: "+err.Error())
			continue
		}
		handle(ctx, s, c, respond, write, &inflight)
	}
	// stdin closed: let running prompts and commands finish before exiting.
	inflight.Wait()
	s.Agent.WaitForIdle()
	return sc.Err()
}

func handle(ctx context.Context, s *runtime.Session, c command, respond func(command, bool, any, string), write func(any), inflight *sync.WaitGroup) {
	ok := func(data any) { respond(c, true, data, "") }
	fail := func(err error) { respond(c, false, nil, err.Error()) }
	switch c.Type {
	case "prompt":
		msg := ai.UserMessage(c.Message)
		for _, im := range c.Images {
			msg.Content = append(msg.Content, ai.Content{Type: "image", Data: im.Data, MimeType: im.MimeType})
		}
		if s.IsRunning() {
			// Queued through the session so extension input hooks still run.
			if c.StreamingBehavior == "followUp" {
				s.FollowUpMessage(msg)
			} else {
				s.SteerMessage(msg)
			}
			ok(nil)
			return
		}
		text, handled, err := s.ExpandInput(c.Message)
		if err != nil {
			fail(err)
			return
		}
		if handled {
			if strings.TrimSpace(text) == "" {
				ok(nil)
				return
			}
			msg.Content[0].Text = text
		}
		ok(nil)
		inflight.Add(1)
		go func() { defer inflight.Done(); _ = s.PromptMessage(ctx, msg) }()
	case "steer":
		s.SteerMessage(ai.UserMessage(c.Message))
		ok(nil)
	case "follow_up":
		s.FollowUpMessage(ai.UserMessage(c.Message))
		ok(nil)
	case "abort":
		s.Abort()
		s.Agent.WaitForIdle()
		ok(nil)
	case "clear_queue":
		st, fu := s.Agent.ClearQueue()
		ok(map[string]any{"steering": texts(st), "followUp": texts(fu)})
	case "get_state":
		st, fu := s.Agent.QueueTexts()
		ok(map[string]any{
			"model": s.Model(), "thinkingLevel": s.ThinkingLevel(), "isStreaming": s.IsRunning(),
			"steeringMode": s.Agent.SteeringMode, "followUpMode": s.Agent.FollowUpMode,
			"sessionFile": s.Store.Path(), "sessionId": s.Store.ID(), "sessionName": s.Store.Name(),
			"autoCompactionEnabled": s.AutoCompaction(), "messageCount": len(s.Messages()),
			"pendingMessageCount": len(st) + len(fu),
		})
	case "get_messages":
		ok(s.Messages())
	case "set_model":
		m, found := s.Registry.Find(c.Provider + "/" + c.ModelID)
		if !found {
			fail(fmt.Errorf("unknown model %s/%s", c.Provider, c.ModelID))
			return
		}
		s.SetModel(m)
		ok(m)
	case "cycle_model":
		ok(s.CycleModel(false))
	case "get_available_models":
		ok(s.Registry.Available())
	case "set_thinking_level":
		if err := s.SetThinkingLevel(c.Level); err != nil {
			fail(err)
			return
		}
		ok(nil)
	case "cycle_thinking_level":
		ok(map[string]any{"level": s.CycleThinkingLevel()})
	case "get_available_thinking_levels":
		ok(ai.ThinkingLevels)
	case "set_steering_mode":
		s.Agent.SteeringMode = c.Mode
		ok(nil)
	case "set_follow_up_mode":
		s.Agent.FollowUpMode = c.Mode
		ok(nil)
	case "compact":
		summary, err := s.Compact(ctx, c.CustomInstructions)
		if err != nil {
			fail(err)
			return
		}
		ok(map[string]any{"summary": summary})
	case "set_auto_compaction":
		s.SetAutoCompaction(c.Enabled == nil || *c.Enabled)
		ok(nil)
	case "bash":
		inflight.Add(1)
		go func() {
			defer inflight.Done()
			output, code, err := s.RunBash(ctx, c.Command, func(p []byte) {
				write(map[string]any{"type": "bash_execution_update", "id": c.ID, "delta": string(p)})
			})
			if err != nil {
				fail(err)
				return
			}
			ok(map[string]any{"output": output, "exitCode": code, "cancelled": false, "truncated": false})
		}()
	case "new_session":
		s.NewSession()
		ok(map[string]any{"sessionFile": s.Store.Path(), "sessionId": s.Store.ID()})
	case "switch_session":
		if err := s.SwitchSession(c.SessionPath); err != nil {
			fail(err)
			return
		}
		ok(map[string]any{"sessionFile": s.Store.Path()})
	case "fork":
		text, err := s.Fork(c.EntryID)
		if err != nil {
			fail(err)
			return
		}
		ok(map[string]any{"text": text, "sessionFile": s.Store.Path()})
	case "clone":
		ok(map[string]any{"sessionFile": s.Clone()})
	case "get_fork_messages":
		ok(s.ForkPoints())
	case "get_entries":
		ok(map[string]any{"entries": s.Store.Entries, "leafId": s.Store.LeafID()})
	case "get_tree":
		ok(tree(s))
	case "navigate_tree":
		if err := s.NavigateTree(c.EntryID); err != nil {
			fail(err)
			return
		}
		ok(nil)
	case "get_last_assistant_text":
		ok(map[string]any{"text": s.LastAssistantText()})
	case "set_session_name":
		s.SetName(c.Name)
		ok(nil)
	case "get_session_stats":
		ok(s.Stats())
	case "export_html":
		path := c.OutputPath
		if path == "" {
			path = "pig-session-" + s.Store.ID()[:8] + ".html"
		}
		if err := export.HTML(path, "pig session", s.Store.Messages()); err != nil {
			fail(err)
			return
		}
		ok(map[string]any{"path": path})
	case "get_commands":
		ok(s.Commands())
	case "reload":
		s.Reload()
		ok(nil)
	default:
		respond(c, false, nil, "unknown command: "+c.Type)
	}
}

func texts(ms []ai.Message) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.TextContent())
	}
	return out
}

type node struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Preview  string  `json:"preview,omitempty"`
	Children []*node `json:"children"`
}

func tree(s *runtime.Session) []*node {
	byID := map[string]*node{}
	var roots []*node
	for _, e := range s.Store.Entries {
		n := &node{ID: e.ID, Type: e.Type, Children: []*node{}}
		if e.Message != nil {
			n.Type = e.Message.Role
			p := e.Message.TextContent()
			if len(p) > 60 {
				p = p[:60] + "..."
			}
			n.Preview = p
		}
		byID[e.ID] = n
		if e.ParentID == nil {
			roots = append(roots, n)
		} else if parent := byID[*e.ParentID]; parent != nil {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
}
