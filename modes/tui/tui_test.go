package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/runtime"
	"github.com/tobyjackson/pig/tools"
)

// newTestModel builds a model wired to a real Session with a scripted
// stream, so no network is touched.
func newTestModel(t *testing.T, stream ai.StreamFunc) *model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	s, err := runtime.New(runtime.Options{
		Cwd: t.TempDir(), Model: "claude-opus-5", Mode: "tui", Stream: stream,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ta := textarea.New()
	ta.SetHeight(3)
	m := &model{s: s, events: make(chan runtime.Event, 256), ta: ta, follow: true, showThink: true}
	s.Subscribe(func(e runtime.Event) { m.events <- e })
	return m
}

// scriptedStream replies with the next string in the list, then "done".
func scriptedStream(replies []string) ai.StreamFunc {
	i := 0
	return func(ctx context.Context, m ai.Model, c ai.Context, o ai.Options) <-chan ai.Event {
		out := make(chan ai.Event, 8)
		go func() {
			defer close(out)
			reply := "done"
			if i < len(replies) {
				reply = replies[i]
			}
			i++
			msg := &ai.Message{Role: "assistant", Provider: m.Provider, Model: m.ID, Usage: &ai.Usage{Input: 10, Output: 5}, Timestamp: ai.Now()}
			out <- ai.Event{Type: "start", Partial: msg}
			msg.Content = []ai.Content{ai.Text(reply)}
			msg.StopReason = ai.StopStop
			out <- ai.Event{Type: "text_delta", Delta: reply, Partial: msg}
			out <- ai.Event{Type: "done", Reason: msg.StopReason, Message: msg}
		}()
		return out
	}
}

func TestAddMessageBuildsBlocks(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))

	m.addMessage(ai.UserMessage("hello"))
	m.addMessage(ai.Message{Role: "assistant", Content: []ai.Content{
		{Type: "thinking", Thinking: "hmm"},
		ai.Text("hi there"),
		{Type: "toolCall", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}})
	m.addMessage(ai.Message{Role: "toolResult", ToolName: "bash", Content: []ai.Content{ai.Text("a.go")}})

	kinds := make([]string, len(m.blocks))
	for i, b := range m.blocks {
		kinds[i] = b.kind
	}
	want := []string{"user", "thinking", "text", "tool"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("block kinds = %v, want %v", kinds, want)
	}
	if got := m.blocks[3].output; got != "a.go" {
		t.Errorf("tool output = %q, want a.go", got)
	}
}

func TestAddMessageErrorBlock(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.addMessage(ai.Message{Role: "assistant", StopReason: ai.StopError, ErrorMessage: "boom"})
	if n := len(m.blocks); n != 1 || m.blocks[0].kind != "error" || m.blocks[0].text != "boom" {
		t.Fatalf("blocks = %+v, want one error block", m.blocks)
	}
}

func TestHandleEventStreamsAndFinalisesToolBlocks(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))

	m.handleEvent(runtime.Event{Type: "agent_start"})
	if !m.running {
		t.Fatal("agent_start should set running")
	}

	m.handleEvent(runtime.Event{Type: "message_start"})
	m.handleEvent(runtime.Event{Type: "message_update", Update: &ai.Event{Type: "thinking_delta", Delta: "pon"}})
	m.handleEvent(runtime.Event{Type: "message_update", Update: &ai.Event{Type: "text_delta", Delta: "der"}})
	if m.liveTh == nil || m.liveTh.text != "pon" {
		t.Fatalf("live thinking = %+v, want pon", m.liveTh)
	}
	if m.live == nil || m.live.text != "der" {
		t.Fatalf("live text = %+v, want der", m.live)
	}

	// A finished assistant turn with a tool call reopens the tool block.
	m.handleEvent(runtime.Event{Type: "message_end", Message: &ai.Message{Role: "assistant", Content: []ai.Content{
		{Type: "toolCall", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}}})
	if m.live != nil || m.liveTh != nil {
		t.Fatal("message_end should clear live blocks")
	}
	var tool *block
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" {
			tool = &m.blocks[i]
		}
	}
	if tool == nil || tool.done {
		t.Fatalf("tool block = %+v, want pending", tool)
	}

	res := tools.TextResult("out")
	m.handleEvent(runtime.Event{Type: "tool_execution_end", Result: &res})
	if tool == nil || !tool.done || tool.output != "out" {
		t.Fatalf("tool block after end = %+v, want done with out", tool)
	}

	m.handleEvent(runtime.Event{Type: "agent_end"})
	if m.running {
		t.Fatal("agent_end should clear running")
	}
}

func TestSubmitEmptyDoesNothing(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.ta.SetValue("   \n")
	if _, cmd := m.submit(false); cmd != nil {
		t.Fatal("empty submit should return no command")
	}
	if m.running {
		t.Fatal("empty submit should not start a run")
	}
}

func TestSubmitRunsPrompt(t *testing.T) {
	m := newTestModel(t, scriptedStream([]string{"hello back"}))
	m.ta.SetValue("hello")
	_, cmd := m.submit(false)
	if cmd == nil {
		t.Fatal("submit should return a command")
	}
	if !m.running {
		t.Fatal("submit should mark the model running")
	}
	if got := m.blocks[0]; got.kind != "user" || got.text != "hello" {
		t.Fatalf("first block = %+v, want the user message", got)
	}

	// The command returns the prompt result; drain the events the session
	// pushed while it ran and feed them back like the event loop would.
	msg := cmd()
	for {
		select {
		case e := <-m.events:
			m.handleEvent(e)
		default:
			goto drained
		}
	}
drained:
	if done, ok := msg.(promptDoneMsg); !ok || done.err != nil {
		t.Fatalf("prompt command returned %#v", msg)
	}
	m.Update(msg)
	var found bool
	for _, b := range m.blocks {
		if b.kind == "text" && b.text == "hello back" {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant text missing from blocks: %+v", m.blocks)
	}
}

func TestSlashCommandHelpAndUnknown(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))

	handled, _ := m.slashCommand("/help")
	if !handled || len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "/model") {
		t.Fatalf("help = handled %v, blocks %+v", handled, m.blocks)
	}

	handled, cmd := m.slashCommand("/not-a-command")
	if handled || cmd != nil {
		t.Fatalf("unknown command handled=%v cmd=%v, want false/nil", handled, cmd)
	}
}

func TestSlashCommandExport(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	path := filepath.Join(t.TempDir(), "out.html")
	if handled, _ := m.slashCommand("/export " + path); !handled {
		t.Fatal("export should be handled")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export did not write %s: %v", path, err)
	}
}

func TestPickerKeyNavigation(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	selected := -1
	m.pick = &picker{items: []string{"a", "b", "c"}, onSelect: func(i int) tea.Cmd { selected = i; return nil }}

	m.pickerKey("down")
	m.pickerKey("down")
	m.pickerKey("up")
	if m.pick.idx != 1 {
		t.Fatalf("picker idx = %d, want 1", m.pick.idx)
	}
	m.pickerKey("enter")
	if m.pick != nil {
		t.Fatal("enter should close the picker")
	}
	if selected != 1 {
		t.Fatalf("selected = %d, want 1", selected)
	}
}

func TestHandleKeyToggles(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	before := m.showTools
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.showTools == before {
		t.Fatal("ctrl+o should toggle tool output")
	}
	before = m.showThink
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.showThink == before {
		t.Fatal("ctrl+t should toggle thinking")
	}
}

func TestArgsPreview(t *testing.T) {
	cases := []struct {
		name, args, want string
	}{
		{"bash", `{"command":"echo hi"}`, "echo hi"},
		{"read", `{"path":"/tmp/x.go"}`, "/tmp/x.go"},
		{"bash", `not json`, "not json"},
	}
	for _, c := range cases {
		if got := argsPreview(c.name, c.args); got != c.want {
			t.Errorf("argsPreview(%q, %q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// toolBlock builds a finished tool block, the shape a collapsed call has.
func toolBlock(name, args, output string, isErr bool) block {
	return block{kind: "tool", name: name, args: args, output: output, done: true, isErr: isErr}
}

// TestToolLinesFitTheColumn guards the case that used to overflow: a bash
// command longer than the work column was written without the column's wrap
// style, so it ran past the divider and the terminal cut it off.
func TestToolLinesFitTheColumn(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.width, m.height = 120, 30
	m.layout()
	col := m.colWidth(m.rightW())
	if col <= 0 {
		t.Fatal("test needs a wide screen")
	}

	long := strings.Repeat("verylongargument ", 12)
	args, err := json.Marshal(map[string]string{"command": "cd /tmp && " + long})
	if err != nil {
		t.Fatal(err)
	}
	m.blocks = []block{toolBlock("bash", string(args), "ok", false)}

	for _, expanded := range []bool{false, true} {
		m.showTools = expanded
		out := m.renderWork()
		if !strings.Contains(out, "verylongargument") {
			t.Errorf("expanded=%v: tool line vanished:\n%s", expanded, out)
		}
		// padLeft adds the column's margin, so the rendered line may be
		// wider than colWidth but never wider than the column itself.
		for _, l := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(l); w > m.rightW() {
				t.Errorf("expanded=%v: tool line is %d wide, column is %d: %q", expanded, w, m.rightW(), l)
			}
		}
	}
}

// TestAltTHidesToolLines checks the Alt+T state, including the rule that a
// failed tool stays on screen so an error is never silent.
func TestAltTHidesToolLines(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.width, m.height = 120, 30
	m.layout()
	m.blocks = []block{
		toolBlock("read", `{"path":"/tmp/ok.go"}`, "contents", false),
		toolBlock("bash", `{"command":"false"}`, "boom", true),
	}

	if m.hideToolRows {
		t.Fatal("tool rows should start visible")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}, Alt: true})
	if !m.hideToolRows {
		t.Fatal("alt+t should hide tool rows")
	}

	out := m.renderWork()
	if strings.Contains(out, "/tmp/ok.go") {
		t.Errorf("a successful tool line survived alt+t:\n%s", out)
	}
	// The failed tool collapses to its header, so look for the command and
	// the error mark rather than the output, which a collapsed block hides.
	if !strings.Contains(out, "✗") || !strings.Contains(out, "false") {
		t.Errorf("a failed tool line was hidden by alt+t:\n%s", out)
	}

	// Hidden wins over an expanded column, as agreed.
	m.showTools = true
	if out := m.renderWork(); strings.Contains(out, "/tmp/ok.go") {
		t.Errorf("expanded output overrode alt+t:\n%s", out)
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}, Alt: true})
	if m.hideToolRows {
		t.Fatal("alt+t should toggle back")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short truncate = %q", got)
	}
	if got := truncate("hello world", 6); got != "hello…" {
		t.Errorf("long truncate = %q, want hello…", got)
	}
}

func TestSlashHint(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.ta.SetValue("/he")
	if got := m.slashHint(); !strings.Contains(got, "/help") {
		t.Fatalf("slashHint = %q, want /help", got)
	}
	m.ta.SetValue("plain text")
	if got := m.slashHint(); got != "" {
		t.Fatalf("slashHint for plain text = %q, want empty", got)
	}
}

func TestFooterFitsOneLine(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.s.Opts.Cwd = "/Users/toby/code/some/long/project/path"
	m.setStatus("a status that is long enough to matter")
	for _, w := range []int{12, 30, 60, 100, 200} {
		m.width, m.height = w, 30
		m.layout()
		out := m.View()
		if got := len(strings.Split(out, "\n")); got != m.height {
			t.Errorf("width %d: view is %d lines, want %d", w, got, m.height)
		}
		for _, l := range strings.Split(out, "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("width %d: line overflows: %q", w, l)
			}
		}
	}
	// The model key is the last part dropped, so it survives a narrow screen.
	m.width, m.height = 40, 30
	m.layout()
	if !strings.Contains(m.footer(), m.s.Model().Key()) {
		t.Errorf("footer dropped the model key: %q", m.footer())
	}
	if strings.Contains(m.footer(), "/Users/toby/code") {
		t.Errorf("footer kept the cwd at 40 cols: %q", m.footer())
	}
}

func TestViewDump(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.width, m.height = 80, 24
	m.layout()
	m.note("hello transcript")

	dump := filepath.Join(t.TempDir(), "view.txt")
	t.Setenv("PIG_DUMP_VIEW", dump)
	out := m.View()
	if !strings.Contains(out, "hello transcript") {
		t.Fatalf("view missing note:\n%s", out)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("view dump not written: %v", err)
	}
	if !strings.Contains(string(data), "hello transcript") {
		t.Fatal("dumped view missing note")
	}
}

// tornReader delivers a byte stream in fixed-size chunks, so a mouse report
// can be made to straddle a read boundary the way bubbletea's 256-byte buffer
// tears one.
type tornReader struct {
	data []byte
	pos  int
	max  int
}

func (c *tornReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := c.pos + c.max
	if end > len(c.data) {
		end = len(c.data)
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	time.Sleep(10 * time.Millisecond)
	return n, nil
}

// A mouse report that the input buffer splits must not reach the textarea.
// bubbletea v1 parses a torn SGR report as an Alt-'[' plus ordinary runes,
// which the textarea would otherwise insert into the prompt.
func TestMouseReportDoesNotReachTheTextarea(t *testing.T) {
	report := []byte("\x1b[<65;32;30M") // wheel down
	// Pad so the report straddles the 256-byte read boundary.
	blob := append(bytes.Repeat([]byte{'a'}, 256-len(report)+6), report...)

	m := newTestModel(t, scriptedStream(nil))
	m.ta.Focus()
	prog := tea.NewProgram(m, tea.WithInput(&tornReader{data: blob, max: 256}), tea.WithoutRenderer(), tea.WithMouseCellMotion())
	go func() { time.Sleep(400 * time.Millisecond); prog.Quit() }()
	if _, err := prog.Run(); err != nil {
		t.Fatal(err)
	}
	if got := m.ta.Value(); strings.ContainsAny(got, "[<;M") {
		t.Fatalf("a torn mouse report leaked into the prompt: %q", got)
	}
}

// A fragment that looks like the start of a report but never completes must
// still reach the prompt: filtering cannot swallow real typing.
func TestMouseFilterKeepsRealTyping(t *testing.T) {
	m := newTestModel(t, scriptedStream(nil))
	m.ta.Focus()
	// Alt+'[' alone is held as a possible report start...
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true})
	if m.ta.Value() != "" {
		t.Fatalf("held fragment was inserted early: %q", m.ta.Value())
	}
	// ...but ordinary typing flushes it back.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	if got := m.ta.Value(); got != "[hello" {
		t.Fatalf("real typing was lost: got %q, want %q", got, "[hello")
	}
}

// Ordinary typing that happens to look like a mouse-report fragment must not
// be swallowed: a bare "<M>" (a generic type argument) is real text.
func TestMouseFilterKeepsGenericTyping(t *testing.T) {
	for _, s := range []string{"<M>", "<65;32;30M>", "a<Mb"} {
		m := newTestModel(t, scriptedStream(nil))
		m.ta.Focus()
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		if got := m.ta.Value(); got != s {
			t.Errorf("typing %q became %q", s, got)
		}
	}
}
