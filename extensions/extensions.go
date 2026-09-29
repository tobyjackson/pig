// Package extensions runs add-on programs found in ~/.pig/extensions or
// .pig/extensions. Each is any executable, in any language, talking JSON
// lines over stdin/stdout: pig sends init, the extension answers ready
// (tools, commands, events), then requests carry an id the reply echoes.
// Full protocol: docs/extensions.md.
package extensions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/tools"
)

// ToolSpec is a tool an extension offers to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// CommandSpec is a slash command an extension offers to the user.
type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Ready is the extension's reply to init.
type Ready struct {
	Type     string        `json:"type"`
	Name     string        `json:"name"`
	Tools    []ToolSpec    `json:"tools"`
	Commands []CommandSpec `json:"commands"`
	Events   []string      `json:"events"`
}

// Notice is an unsolicited message from an extension for the user.
type Notice struct {
	Extension string
	Message   string
	Level     string
}

// Extension is one running add-on process.
type Extension struct {
	Path  string
	Ready Ready

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	seq     int
	notify  func(Notice)
	done    chan struct{}
}

// Manager owns every loaded extension.
type Manager struct {
	Extensions []*Extension
	Timeout    time.Duration
	Notify     func(Notice)
}

// Discover lists executable files in the standard folders plus extra paths.
func Discover(cwd string, trusted bool, extra []string) []string {
	var dirs []string
	dirs = append(dirs, filepath.Join(globalDir(), "extensions"))
	if trusted {
		dirs = append(dirs, filepath.Join(cwd, ".pig", "extensions"))
	}
	var out []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			p := filepath.Join(d, n)
			if isExecutable(p) {
				out = append(out, p)
			}
		}
	}
	for _, p := range extra {
		if isExecutable(p) {
			out = append(out, p)
		}
	}
	return out
}

func globalDir() string {
	if d := os.Getenv("PIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pig")
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// Load starts each extension and completes the handshake. Failures are
// reported through Notify and skipped.
func Load(ctx context.Context, paths []string, cwd string, notify func(Notice)) *Manager {
	m := &Manager{Timeout: 30 * time.Second, Notify: notify}
	for _, p := range paths {
		ext, err := start(ctx, p, cwd, notify)
		if err != nil {
			if notify != nil {
				notify(Notice{Extension: filepath.Base(p), Message: err.Error(), Level: "error"})
			}
			continue
		}
		m.Extensions = append(m.Extensions, ext)
	}
	return m
}

func start(ctx context.Context, path, cwd string, notify func(Notice)) (*Extension, error) {
	cmd := exec.Command(path)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PIG_EXTENSION=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", path, err)
	}
	ext := &Extension{Path: path, cmd: cmd, stdin: stdin, pending: map[string]chan json.RawMessage{}, notify: notify, done: make(chan struct{})}
	readyCh := make(chan Ready, 1)
	go ext.readLoop(stdout, readyCh)
	if err := ext.send(map[string]any{"type": "init", "cwd": cwd, "protocol": 1}); err != nil {
		return nil, err
	}
	select {
	case r := <-readyCh:
		ext.Ready = r
		if ext.Ready.Name == "" {
			ext.Ready.Name = filepath.Base(path)
		}
	case <-time.After(15 * time.Second):
		ext.Close()
		return nil, fmt.Errorf("%s did not answer init within 15s", filepath.Base(path))
	case <-ext.done:
		return nil, fmt.Errorf("%s exited during init", filepath.Base(path))
	}
	return ext, nil
}

func (e *Extension) readLoop(r io.Reader, readyCh chan Ready) {
	defer close(e.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		var head struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
			Level   string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &head) != nil {
			continue
		}
		switch head.Type {
		case "ready":
			var r Ready
			_ = json.Unmarshal([]byte(line), &r)
			select {
			case readyCh <- r:
			default:
			}
		case "notify":
			if e.notify != nil {
				e.notify(Notice{Extension: e.Ready.Name, Message: head.Message, Level: head.Level})
			}
		default:
			e.mu.Lock()
			ch := e.pending[head.ID]
			e.mu.Unlock()
			if ch != nil {
				ch <- json.RawMessage(line)
			}
		}
	}
}

func (e *Extension) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.stdin.Write(append(data, '\n'))
	return err
}

// request sends a message with a fresh id and waits for the reply.
func (e *Extension) request(v map[string]any, timeout time.Duration) (json.RawMessage, error) {
	e.mu.Lock()
	e.seq++
	id := fmt.Sprintf("%d", e.seq)
	ch := make(chan json.RawMessage, 1)
	e.pending[id] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.pending, id)
		e.mu.Unlock()
	}()
	v["id"] = id
	if err := e.send(v); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: no reply within %s", e.Ready.Name, timeout)
	case <-e.done:
		return nil, fmt.Errorf("%s exited", e.Ready.Name)
	}
}

// Close stops the process.
func (e *Extension) Close() {
	_ = e.send(map[string]any{"type": "shutdown"})
	e.stdin.Close()
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		_ = e.cmd.Process.Kill()
	}
	_ = e.cmd.Wait()
}

func (e *Extension) handles(event string) bool {
	for _, ev := range e.Ready.Events {
		if ev == event {
			return true
		}
	}
	return false
}

// Close stops every extension.
func (m *Manager) Close() {
	for _, e := range m.Extensions {
		e.Close()
	}
}

// Emit sends an event to every extension that asked for it and merges
// their replies (later extensions see earlier changes). The reply is a
// JSON object; fields depend on the event.
func (m *Manager) Emit(event string, data map[string]any) map[string]json.RawMessage {
	merged := map[string]json.RawMessage{}
	for _, e := range m.Extensions {
		if !e.handles(event) {
			continue
		}
		reply, err := e.request(map[string]any{"type": "event", "event": event, "data": data}, m.Timeout)
		if err != nil {
			m.notice(e, err.Error(), "error")
			continue
		}
		var wrap struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(reply, &wrap) == nil {
			for k, v := range wrap.Data {
				merged[k] = v
				// Let the next extension see the change.
				var anyv any
				_ = json.Unmarshal(v, &anyv)
				data[k] = anyv
			}
		}
	}
	return merged
}

func (m *Manager) notice(e *Extension, msg, level string) {
	if m.Notify != nil {
		m.Notify(Notice{Extension: e.Ready.Name, Message: msg, Level: level})
	}
}

// Tools returns every tool offered by extensions, wrapped for the agent.
func (m *Manager) Tools() []tools.Tool {
	var out []tools.Tool
	for _, e := range m.Extensions {
		for _, spec := range e.Ready.Tools {
			out = append(out, &extTool{ext: e, spec: spec, timeout: m.Timeout})
		}
	}
	return out
}

// Commands returns every slash command offered by extensions.
func (m *Manager) Commands() []struct {
	Ext  *Extension
	Spec CommandSpec
} {
	var out []struct {
		Ext  *Extension
		Spec CommandSpec
	}
	for _, e := range m.Extensions {
		for _, c := range e.Ready.Commands {
			out = append(out, struct {
				Ext  *Extension
				Spec CommandSpec
			}{e, c})
		}
	}
	return out
}

// CommandResult is what a command handler returned.
type CommandResult struct {
	Message string `json:"message"` // text to send to the model as the user
	Notify  string `json:"notify"`  // text to show the user
}

// RunCommand asks the owning extension to handle a slash command.
func (m *Manager) RunCommand(name, args string) (CommandResult, bool, error) {
	for _, c := range m.Commands() {
		if c.Spec.Name != name {
			continue
		}
		reply, err := c.Ext.request(map[string]any{"type": "command", "name": name, "args": args}, m.Timeout)
		if err != nil {
			return CommandResult{}, true, err
		}
		var wrap struct {
			Data CommandResult `json:"data"`
		}
		_ = json.Unmarshal(reply, &wrap)
		return wrap.Data, true, nil
	}
	return CommandResult{}, false, nil
}

type extTool struct {
	ext     *Extension
	spec    ToolSpec
	timeout time.Duration
}

func (t *extTool) Name() string        { return t.spec.Name }
func (t *extTool) Description() string { return t.spec.Description }
func (t *extTool) Parameters() json.RawMessage {
	if len(t.spec.Parameters) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.spec.Parameters
}

func (t *extTool) Execute(ctx context.Context, callID string, args json.RawMessage, onUpdate func(tools.Result)) tools.Result {
	var input any
	_ = json.Unmarshal(args, &input)
	reply, err := t.ext.request(map[string]any{"type": "tool_call", "name": t.spec.Name, "toolCallId": callID, "input": input}, 10*time.Minute)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	var res struct {
		Content []ai.Content `json:"content"`
		Text    string       `json:"text"`
		IsError bool         `json:"isError"`
		Details any          `json:"details"`
	}
	if err := json.Unmarshal(reply, &res); err != nil {
		return tools.ErrorResult("bad tool reply from " + t.ext.Ready.Name)
	}
	if len(res.Content) == 0 && res.Text != "" {
		res.Content = []ai.Content{ai.Text(res.Text)}
	}
	return tools.Result{Content: res.Content, Details: res.Details, IsError: res.IsError}
}
