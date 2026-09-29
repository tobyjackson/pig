// Package tui is the interactive terminal screen: a scrolling transcript,
// an editor at the bottom, and a status line.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/modes/export"
	"github.com/tobyjackson/pig/runtime"
)

// Styles used across the screen.
var (
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	userSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	userText = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	asstSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	thinkSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	toolSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	toolOut  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	noteSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	footerSt = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("236"))
	pickSt   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	selSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	edge     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// padX is the left/right margin applied to transcript blocks so text does not
// touch the screen edge.
const padX = 2

// padLeft adds the left margin and optional dim gutter marker.
func padLeft(s string, marker bool) string {
	indent := strings.Repeat(" ", padX)
	if marker {
		indent = edge.Render("\u2502") + strings.Repeat(" ", padX-1)
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// block is one rendered piece of the transcript.
type block struct {
	kind string // user | text | thinking | tool | note | error | bash
	text string
	// tool blocks
	name   string
	args   string
	output string
	isErr  bool
	done   bool
}

type eventMsg runtime.Event
type promptDoneMsg struct{ err error }
type bashDoneMsg struct {
	command string
	output  string
	code    int
	record  bool
	err     error
}
type bashChunkMsg string
type editorDoneMsg struct {
	path string
	err  error
}

// picker is a simple list overlay.
type picker struct {
	title    string
	items    []string
	idx      int
	onSelect func(i int) tea.Cmd
	onSave   func(i int) tea.Cmd // Ctrl+S
}

type model struct {
	s            *runtime.Session
	events       chan runtime.Event
	vp           viewport.Model
	ta           textarea.Model
	width        int
	height       int
	blocks       []block
	live         *block // assistant text being streamed
	liveTh       *block // thinking being streamed
	running      bool
	status       string
	statusAt     time.Time
	showTools    bool
	showThink    bool
	lastCtrlC    time.Time
	lastEsc      time.Time
	pick         *picker
	follow       bool // viewport follows new output
	spinner      int
	quiet        bool
	pendingLogin string
}

// Run starts the interactive screen and blocks until the user quits.
func Run(s *runtime.Session, quietStartup bool) error {
	// Skip the terminal background-colour query: it can stall startup for
	// seconds on terminals that never answer, and our colours suit dark
	// and light backgrounds well enough.
	lipgloss.SetHasDarkBackground(true)
	ta := textarea.New()
	ta.Placeholder = "Ask anything. Enter sends, Ctrl+J adds a line, / for commands, ! for shell."
	ta.ShowLineNumbers = false
	ta.Prompt = "> "
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.Focus()
	m := &model{s: s, events: make(chan runtime.Event, 256), ta: ta, showTools: false,
		showThink: s.Settings.HideThinkingBlock == nil || !*s.Settings.HideThinkingBlock, follow: true, quiet: quietStartup}
	s.Subscribe(func(e runtime.Event) { m.events <- e })
	m.loadHistory()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func (m *model) loadHistory() {
	for _, msg := range m.s.Messages() {
		m.addMessage(msg)
	}
	if !m.quiet {
		m.blocks = append(m.blocks, block{kind: "note", text: m.header()})
	}
}

func (m *model) header() string {
	st := m.s.Stats()
	lines := []string{
		fmt.Sprintf("pig · %s · thinking %s", m.s.Model().Key(), m.s.ThinkingLevel()),
		fmt.Sprintf("cwd %s", m.s.Opts.Cwd),
	}
	if st.SessionFile != "" {
		lines = append(lines, "session "+st.SessionFile)
	} else {
		lines = append(lines, "session: not saved (--no-session)")
	}
	if n := len(m.s.Skills); n > 0 {
		lines = append(lines, fmt.Sprintf("%d skill(s), %d prompt template(s)", n, len(m.s.Prompts)))
	}
	if m.s.Exts != nil && len(m.s.Exts.Extensions) > 0 {
		var names []string
		for _, e := range m.s.Exts.Extensions {
			names = append(names, e.Ready.Name)
		}
		lines = append(lines, "extensions: "+strings.Join(names, ", "))
	}
	lines = append(lines, "/help for commands, /hotkeys for keys")
	return strings.Join(lines, "\n")
}

// addMessage converts a stored message into transcript blocks.
func (m *model) addMessage(msg ai.Message) {
	switch msg.Role {
	case "user":
		m.blocks = append(m.blocks, block{kind: "user", text: msg.TextContent()})
	case "assistant":
		for _, c := range msg.Content {
			switch c.Type {
			case "thinking":
				if strings.TrimSpace(c.Thinking) != "" {
					m.blocks = append(m.blocks, block{kind: "thinking", text: c.Thinking})
				}
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					m.blocks = append(m.blocks, block{kind: "text", text: c.Text})
				}
			case "toolCall":
				m.blocks = append(m.blocks, block{kind: "tool", name: c.Name, args: string(c.Arguments), done: true})
			}
		}
		if msg.StopReason == ai.StopError || msg.StopReason == ai.StopAborted {
			m.blocks = append(m.blocks, block{kind: "error", text: msg.ErrorMessage})
		}
	case "toolResult":
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == "tool" && m.blocks[i].output == "" && m.blocks[i].name == msg.ToolName {
				m.blocks[i].output = msg.TextContent()
				m.blocks[i].isErr = msg.IsError
				break
			}
		}
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.waitEvent(), textarea.Blink, tick())
}

func tick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}

func (m *model) waitEvent() tea.Cmd {
	return func() tea.Msg { return eventMsg(<-m.events) }
}

func (m *model) setStatus(s string) {
	m.status = s
	m.statusAt = time.Now()
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	m.ta.SetWidth(m.width - 2*padX)
	taH := m.ta.Height()
	extra := 0
	if m.pick != nil {
		extra = 0
	}
	h := m.height - taH - 2 - extra // footer + spacer
	if h < 3 {
		h = 3
	}
	if m.vp.Width == 0 {
		m.vp = viewport.New(m.width, h)
	}
	m.vp.Width = m.width
	m.vp.Height = h
	m.refresh()
}

// refresh re-renders the transcript into the viewport.
func (m *model) refresh() {
	if m.width == 0 {
		return
	}
	m.vp.SetContent(m.render())
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) render() string {
	var sb strings.Builder
	w := m.width - 2*padX
	if w < 20 {
		w = 20
	}
	wrap := lipgloss.NewStyle().Width(w)
	for _, b := range m.blocks {
		m.renderBlock(&sb, b, wrap)
	}
	if m.liveTh != nil {
		m.renderBlock(&sb, *m.liveTh, wrap)
	}
	if m.live != nil {
		m.renderBlock(&sb, *m.live, wrap)
	}
	if m.running {
		sb.WriteString(padLeft(dim.Render(spinnerFrames[m.spinner%len(spinnerFrames)]+" working… (Esc to stop)"), false))
		sb.WriteString("\n")
	}
	return sb.String()
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m *model) renderBlock(sb *strings.Builder, b block, wrap lipgloss.Style) {
	switch b.kind {
	case "user":
		sb.WriteString(padLeft(userSt.Render("you"), false) + "\n")
		sb.WriteString(padLeft(wrap.Render(userText.Render(b.text)), true) + "\n\n")
	case "text":
		sb.WriteString(padLeft(asstSt.Render("pig"), false) + "\n")
		sb.WriteString(padLeft(wrap.Render(asstSt.Render(b.text)), true) + "\n\n")
	case "thinking":
		if m.showThink {
			sb.WriteString(padLeft(wrap.Render(thinkSt.Render("thinking: "+strings.TrimSpace(b.text))), false) + "\n\n")
		} else {
			sb.WriteString(padLeft(dim.Render("thinking… (Ctrl+T to show)"), false) + "\n\n")
		}
	case "tool":
		head := toolSt.Render("▶ " + b.name + " " + argsPreview(b.name, b.args))
		sb.WriteString(padLeft(head, false) + "\n")
		if b.output != "" {
			out := b.output
			lines := strings.Split(out, "\n")
			if !m.showTools && len(lines) > 8 {
				out = strings.Join(lines[:8], "\n") + dim.Render(fmt.Sprintf("\n… %d more lines (Ctrl+O to expand)", len(lines)-8))
			}
			st := toolOut
			if b.isErr {
				st = errSt
			}
			sb.WriteString(padLeft(wrap.Render(st.Render(out)), false) + "\n")
		} else if !b.done {
			sb.WriteString(padLeft(dim.Render("running…"), false) + "\n")
		}
		sb.WriteString("\n")
	case "note":
		sb.WriteString(padLeft(wrap.Render(noteSt.Render(b.text)), false) + "\n\n")
	case "error":
		sb.WriteString(padLeft(wrap.Render(errSt.Render("error: "+b.text)), true) + "\n\n")
	case "bash":
		sb.WriteString(padLeft(toolSt.Render("$ "+b.name), false) + "\n" + padLeft(wrap.Render(toolOut.Render(b.output)), false) + "\n\n")
	}
}

// argsPreview shows the most useful argument for a tool call in one line.
func argsPreview(name, args string) string {
	var a map[string]any
	if json.Unmarshal([]byte(args), &a) != nil {
		return truncate(args, 100)
	}
	switch name {
	case "bash":
		if c, ok := a["command"].(string); ok {
			return truncate(strings.ReplaceAll(c, "\n", " ; "), 120)
		}
	case "read", "write", "edit":
		if p, ok := a["path"].(string); ok {
			return p
		}
	}
	return truncate(args, 100)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (m *model) footer() string {
	st := m.s.Stats()
	q1, q2 := m.s.Agent.QueueTexts()
	parts := []string{
		m.s.Model().Key(),
		"think:" + m.s.ThinkingLevel(),
		fmt.Sprintf("ctx %.0f%%", st.ContextPercent),
		fmt.Sprintf("$%.4f", st.Tokens.Cost),
	}
	if name := m.s.Store.Name(); name != "" {
		parts = append(parts, name)
	}
	if n := len(q1) + len(q2); n > 0 {
		parts = append(parts, fmt.Sprintf("queued %d", n))
	}
	if m.status != "" && time.Since(m.statusAt) < 8*time.Second {
		parts = append(parts, m.status)
	}
	line := " " + strings.Join(parts, "  │  ")
	return footerSt.Width(m.width).Render(line)
}

func (m *model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	out := m.view()
	if p := os.Getenv("PIG_DUMP_VIEW"); p != "" {
		_ = os.WriteFile(p, []byte(out), 0o644)
	}
	return out
}

func (m *model) view() string {
	var sb strings.Builder
	sb.WriteString(m.vp.View())
	sb.WriteString("\n")
	if m.pick != nil {
		sb.WriteString(m.pick.view(m.width))
	} else {
		sb.WriteString(padLeft(m.ta.View(), false))
		if hint := m.slashHint(); hint != "" {
			sb.WriteString("\n" + padLeft(dim.Render(hint), false))
		}
	}
	sb.WriteString("\n" + m.footer())
	return sb.String()
}

func (p *picker) view(width int) string {
	var sb strings.Builder
	sb.WriteString(selSt.Render(p.title) + "\n")
	start := 0
	max := 8
	if p.idx >= max {
		start = p.idx - max + 1
	}
	for i := start; i < len(p.items) && i < start+max; i++ {
		line := "  " + p.items[i]
		if i == p.idx {
			line = selSt.Render("› " + p.items[i])
		}
		sb.WriteString(truncate(line, width-6) + "\n")
	}
	sb.WriteString(dim.Render("↑/↓ move · Enter choose · Esc cancel"))
	return pickSt.Width(width - 4).Render(sb.String())
}

// slashHint shows matching commands while typing "/...".
func (m *model) slashHint() string {
	v := m.ta.Value()
	if !strings.HasPrefix(v, "/") || strings.Contains(v, " ") || strings.Contains(v, "\n") {
		return ""
	}
	prefix := v[1:]
	var matches []string
	for _, c := range m.allCommands() {
		if strings.HasPrefix(c.Name, prefix) {
			d := c.Description
			if d != "" {
				d = " — " + truncate(d, 50)
			}
			matches = append(matches, "/"+c.Name+d)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	if len(matches) > 6 {
		matches = append(matches[:6], fmt.Sprintf("… %d more", len(matches)-6))
	}
	return strings.Join(matches, "\n")
}

var builtinCommands = []runtime.Command{
	{Name: "model", Description: "pick a model (/model <name> to switch directly)"},
	{Name: "thinking", Description: "set thinking level: off minimal low medium high xhigh max"},
	{Name: "new", Description: "start a new session"},
	{Name: "resume", Description: "pick an earlier session"},
	{Name: "compact", Description: "summarise older history now"},
	{Name: "session", Description: "show session info and totals"},
	{Name: "name", Description: "name this session"},
	{Name: "tree", Description: "go back to an earlier point"},
	{Name: "fork", Description: "start a new session from an earlier message"},
	{Name: "clone", Description: "copy this conversation into a new session"},
	{Name: "export", Description: "write the session as HTML"},
	{Name: "copy", Description: "copy the last reply to the clipboard"},
	{Name: "reload", Description: "reload skills, prompts, extensions, context"},
	{Name: "login", Description: "save an API key: /login anthropic"},
	{Name: "trust", Description: "trust this project's .pig folder"},
	{Name: "hotkeys", Description: "show keyboard shortcuts"},
	{Name: "help", Description: "list commands"},
	{Name: "quit", Description: "exit pig"},
}

func (m *model) allCommands() []runtime.Command {
	out := append([]runtime.Command{}, builtinCommands...)
	out = append(out, m.s.Commands()...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tickMsg:
		if m.running {
			m.spinner++
			m.refresh()
		}
		return m, tick()
	case eventMsg:
		m.handleEvent(runtime.Event(msg))
		return m, m.waitEvent()
	case promptDoneMsg:
		m.running = false
		m.live, m.liveTh = nil, nil
		if msg.err != nil && msg.err != context.Canceled {
			m.setStatus("error: " + truncate(msg.err.Error(), 60))
		}
		m.refresh()
		return m, nil
	case bashChunkMsg:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == "bash" {
			m.blocks[n-1].output += string(msg)
			m.refresh()
		}
		return m, nil
	case bashDoneMsg:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == "bash" {
			m.blocks[n-1].output = strings.TrimRight(msg.output, "\n")
			if msg.err != nil {
				m.blocks[n-1].output += "\n" + errSt.Render(msg.err.Error())
			} else if msg.code != 0 {
				m.blocks[n-1].output += dim.Render(fmt.Sprintf("\n(exit %d)", msg.code))
			}
		}
		if msg.record && msg.err == nil {
			m.s.RecordBash(msg.command, msg.output, msg.code)
		}
		m.refresh()
		return m, nil
	case editorDoneMsg:
		if msg.err == nil {
			if data, err := os.ReadFile(msg.path); err == nil {
				m.ta.SetValue(strings.TrimRight(string(data), "\n"))
			}
		}
		os.Remove(msg.path)
		return m, nil
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *model) handleEvent(e runtime.Event) {
	switch e.Type {
	case "agent_start":
		m.running = true
		m.follow = true
	case "message_start":
		m.live, m.liveTh = nil, nil
	case "message_update":
		if e.Update == nil {
			break
		}
		switch e.Update.Type {
		case "thinking_delta":
			if m.liveTh == nil {
				m.liveTh = &block{kind: "thinking"}
			}
			m.liveTh.text += e.Update.Delta
		case "text_delta":
			if m.live == nil {
				m.live = &block{kind: "text"}
			}
			m.live.text += e.Update.Delta
		}
	case "message_end":
		m.live, m.liveTh = nil, nil
		if e.Message != nil && e.Message.Role == "assistant" {
			m.addMessage(*e.Message)
			// Tool blocks were just added as done; they fill in on tool_execution_end.
			for i := range m.blocks {
				if m.blocks[i].kind == "tool" && m.blocks[i].output == "" {
					m.blocks[i].done = false
				}
			}
		}
	case "tool_execution_update":
		if e.Partial != nil {
			for i := len(m.blocks) - 1; i >= 0; i-- {
				if m.blocks[i].kind == "tool" && !m.blocks[i].done {
					m.blocks[i].output = textOf(e.Partial.Content)
					break
				}
			}
		}
	case "tool_execution_end":
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == "tool" && !m.blocks[i].done {
				if e.Result != nil {
					m.blocks[i].output = textOf(e.Result.Content)
					m.blocks[i].isErr = e.IsError
				}
				m.blocks[i].done = true
				break
			}
		}
	case "agent_end":
		m.running = false
	case "auto_retry":
		m.setStatus(fmt.Sprintf("retry %d/%d in %ds: %s", e.Attempt, e.MaxAttempts, e.DelayMs/1000, truncate(e.Error, 40)))
	case "compaction_start":
		m.setStatus("compacting…")
	case "compaction_end":
		if e.Error != "" {
			m.setStatus("compaction: " + truncate(e.Error, 50))
		} else {
			m.blocks = append(m.blocks, block{kind: "note", text: "Context compacted. Older history is summarised; the full log stays in the session file."})
			m.setStatus("compacted")
		}
	case "notify":
		m.blocks = append(m.blocks, block{kind: "note", text: e.Text})
	case "queue_update":
	case "model_change", "thinking_level_change":
		m.setStatus(e.Type + ": " + e.Text)
	}
	m.refresh()
}

func textOf(cs []ai.Content) string {
	var sb strings.Builder
	for _, c := range cs {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		} else if c.Type == "image" {
			sb.WriteString("[image]")
		}
	}
	return sb.String()
}

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.pick != nil {
		return m.pickerKey(key)
	}
	switch key {
	case "ctrl+c":
		if time.Since(m.lastCtrlC) < 800*time.Millisecond || m.ta.Value() == "" && !m.running {
			m.s.Close()
			return m, tea.Quit
		}
		m.lastCtrlC = time.Now()
		m.ta.Reset()
		m.setStatus("Ctrl+C again to quit")
		return m, nil
	case "ctrl+d":
		m.s.Close()
		return m, tea.Quit
	case "esc":
		if m.running {
			m.s.Abort()
			m.setStatus("aborted")
			return m, nil
		}
		if time.Since(m.lastEsc) < 600*time.Millisecond {
			m.lastEsc = time.Time{}
			return m, m.openTree()
		}
		m.lastEsc = time.Now()
		return m, nil
	case "enter":
		return m.submit(false)
	case "alt+enter":
		return m.submit(true)
	case "ctrl+j", "shift+enter":
		m.ta.InsertString("\n")
		m.growEditor()
		return m, nil
	case "ctrl+l":
		return m, m.openModelPicker()
	case "ctrl+p":
		mo := m.s.CycleModel(false)
		m.setStatus("model: " + mo.Key())
		return m, nil
	case "ctrl+shift+p":
		mo := m.s.CycleModel(true)
		m.setStatus("model: " + mo.Key())
		return m, nil
	case "shift+tab":
		l := m.s.CycleThinkingLevel()
		m.setStatus("thinking: " + l)
		return m, nil
	case "ctrl+o":
		m.showTools = !m.showTools
		m.refresh()
		return m, nil
	case "ctrl+t":
		m.showThink = !m.showThink
		m.refresh()
		return m, nil
	case "ctrl+x":
		m.copyLast()
		return m, nil
	case "ctrl+g":
		return m, m.openExternalEditor()
	case "tab":
		m.completePath()
		return m, nil
	case "pgup", "pgdown", "ctrl+up", "ctrl+down":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		m.follow = m.vp.AtBottom()
		return m, cmd
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(k)
	m.growEditor()
	return m, cmd
}

func (m *model) growEditor() {
	lines := strings.Count(m.ta.Value(), "\n") + 1
	h := lines
	if h < 3 {
		h = 3
	}
	if h > 10 {
		h = 10
	}
	if h != m.ta.Height() {
		m.ta.SetHeight(h)
		m.layout()
	}
}

func (m *model) pickerKey(key string) (tea.Model, tea.Cmd) {
	p := m.pick
	switch key {
	case "esc", "ctrl+c":
		m.pick = nil
		m.layout()
	case "up", "k":
		if p.idx > 0 {
			p.idx--
		}
	case "down", "j":
		if p.idx < len(p.items)-1 {
			p.idx++
		}
	case "enter":
		m.pick = nil
		m.layout()
		if len(p.items) > 0 && p.onSelect != nil {
			return m, p.onSelect(p.idx)
		}
	case "ctrl+s":
		if p.onSave != nil && len(p.items) > 0 {
			m.pick = nil
			m.layout()
			return m, p.onSave(p.idx)
		}
	}
	return m, nil
}

// submit sends the editor text. followUp queues it for after the run.
func (m *model) submit(followUp bool) (tea.Model, tea.Cmd) {
	text := strings.TrimRight(m.ta.Value(), "\n")
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	m.ta.Reset()
	m.ta.SetHeight(3)
	m.layout()

	if m.pendingLogin != "" {
		return m.submitLogin(text)
	}
	if strings.HasPrefix(text, "!") {
		return m, m.runUserBash(text)
	}
	if strings.HasPrefix(text, "/") {
		if handled, cmd := m.slashCommand(text); handled {
			return m, cmd
		}
	}
	expanded, _, err := m.s.ExpandInput(text)
	if err != nil {
		m.blocks = append(m.blocks, block{kind: "error", text: err.Error()})
		m.refresh()
		return m, nil
	}
	if strings.TrimSpace(expanded) == "" {
		return m, nil
	}
	if m.running {
		if followUp {
			m.s.FollowUp(expanded)
			m.setStatus("queued as follow-up")
		} else {
			m.s.Steer(expanded)
			m.setStatus("queued: delivered after current tool calls")
		}
		return m, nil
	}
	m.blocks = append(m.blocks, block{kind: "user", text: text})
	m.running = true
	m.follow = true
	m.refresh()
	return m, func() tea.Msg { return promptDoneMsg{m.s.Prompt(context.Background(), expanded)} }
}

func (m *model) runUserBash(text string) tea.Cmd {
	record := true
	cmdText := strings.TrimPrefix(text, "!")
	if strings.HasPrefix(cmdText, "!") {
		record = false
		cmdText = strings.TrimPrefix(cmdText, "!")
	}
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		return nil
	}
	m.blocks = append(m.blocks, block{kind: "bash", name: cmdText})
	m.refresh()
	return func() tea.Msg {
		output, code, err := m.s.RunBash(context.Background(), cmdText, nil)
		return bashDoneMsg{command: cmdText, output: output, code: code, record: record, err: err}
	}
}

func (m *model) note(s string) {
	m.blocks = append(m.blocks, block{kind: "note", text: s})
	m.refresh()
}

// slashCommand handles built-in commands. Returns false to fall through to
// skills, prompt templates and extension commands.
func (m *model) slashCommand(text string) (bool, tea.Cmd) {
	name, args, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	args = strings.TrimSpace(args)
	switch name {
	case "quit", "exit":
		m.s.Close()
		return true, tea.Quit
	case "help":
		var sb strings.Builder
		sb.WriteString("Commands:\n")
		for _, c := range m.allCommands() {
			sb.WriteString(fmt.Sprintf("  /%-14s %s\n", c.Name, c.Description))
		}
		sb.WriteString("  !cmd           run a shell command and share the output with the model\n  !!cmd          run a shell command privately")
		m.note(sb.String())
	case "hotkeys":
		m.note(strings.TrimSpace(`Keys:
  Enter          send (or steer while the model works)
  Alt+Enter      queue a follow-up for after the model finishes
  Ctrl+J         new line in the editor
  Esc            stop the current run; Esc twice opens /tree
  Ctrl+C         clear the editor; twice quits
  Ctrl+L         model picker (Ctrl+S inside it saves the default)
  Ctrl+P         next model
  Shift+Tab      next thinking level
  Ctrl+O         expand/collapse tool output
  Ctrl+T         show/hide thinking
  Ctrl+X         copy the last reply
  Ctrl+G         edit the prompt in $EDITOR
  Tab            complete a file path
  PgUp/PgDn      scroll the transcript`))
	case "model":
		if args == "" {
			return true, m.openModelPicker()
		}
		mo, ok := m.s.Registry.Find(args)
		if !ok {
			m.note("no model matches " + args)
		} else if !m.s.Registry.HasAuth(mo.Provider) {
			m.note("no API key for " + mo.Provider + " (try /login " + mo.Provider + ")")
		} else {
			m.s.SetModel(mo)
			m.note("model: " + mo.Key())
		}
	case "thinking":
		if args == "" {
			m.note("thinking level: " + m.s.ThinkingLevel() + " (levels: " + strings.Join(ai.ThinkingLevels, " ") + ")")
		} else if err := m.s.SetThinkingLevel(args); err != nil {
			m.note(err.Error())
		} else {
			m.note("thinking: " + args)
		}
	case "new":
		m.s.NewSession()
		m.blocks = nil
		m.note("new session" + pathNote(m.s.Store.Path()))
	case "compact":
		m.setStatus("compacting…")
		return true, func() tea.Msg {
			_, err := m.s.Compact(context.Background(), args)
			return promptDoneMsg{err}
		}
	case "session":
		st := m.s.Stats()
		m.note(fmt.Sprintf("session %s\nfile: %s\nname: %s\nmessages: %d user, %d assistant, %d tool calls\ntokens: in %d out %d cache-read %d cache-write %d\ncost: $%.4f\ncontext: %d / %d (%.0f%%)",
			st.SessionID, orDash(st.SessionFile), orDash(st.Name), st.UserMessages, st.AssistantMsgs, st.ToolCalls,
			st.Tokens.Input, st.Tokens.Output, st.Tokens.CacheRead, st.Tokens.CacheWrite, st.Tokens.Cost,
			st.ContextTokens, st.ContextWindow, st.ContextPercent))
	case "name":
		if args == "" {
			m.note("usage: /name <text>")
		} else {
			m.s.SetName(args)
			m.note("session named: " + args)
		}
	case "tree":
		if args != "" {
			if err := m.s.NavigateTree(args); err != nil {
				m.note(err.Error())
			} else {
				m.rebuildFromSession()
				m.note("moved to entry " + args + "; new messages branch from here")
			}
			return true, nil
		}
		return true, m.openTree()
	case "fork":
		return true, m.openFork()
	case "clone":
		p := m.s.Clone()
		m.note("cloned into new session" + pathNote(p))
	case "export":
		path := args
		if path == "" {
			path = "pig-session-" + m.s.Store.ID()[:8] + ".html"
		}
		if err := export.HTML(path, "pig session", m.s.Store.Messages()); err != nil {
			m.note("export failed: " + err.Error())
		} else {
			m.note("exported to " + path)
		}
	case "copy":
		m.copyLast()
	case "reload":
		m.s.Reload()
		m.note(fmt.Sprintf("reloaded: %d skills, %d prompts, %d extensions", len(m.s.Skills), len(m.s.Prompts), extCount(m.s)))
	case "resume":
		return true, m.openResume()
	case "login":
		if args == "" {
			m.note("usage: /login <provider>   then paste the key when asked")
			return true, nil
		}
		return true, m.openLogin(args)
	case "trust":
		_ = m.s.SaveTrustYes()
		m.note("project trusted; restart pig to load its .pig files")
	default:
		return false, nil
	}
	return true, nil
}

func extCount(s *runtime.Session) int {
	if s.Exts == nil {
		return 0
	}
	return len(s.Exts.Extensions)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pathNote(p string) string {
	if p == "" {
		return ""
	}
	return ": " + p
}

func (m *model) rebuildFromSession() {
	m.blocks = nil
	m.loadHistory()
	m.refresh()
}

func (m *model) openModelPicker() tea.Cmd {
	avail := m.s.Registry.Available()
	if len(avail) == 0 {
		m.note("no models with an API key; use /login <provider> or set ANTHROPIC_API_KEY / OPENAI_API_KEY")
		return nil
	}
	items := make([]string, len(avail))
	idx := 0
	for i, mo := range avail {
		items[i] = fmt.Sprintf("%-40s %s", mo.Key(), mo.Name)
		if mo.Key() == m.s.Model().Key() {
			idx = i
		}
	}
	m.pick = &picker{title: "Model (Ctrl+S saves as default)", items: items, idx: idx,
		onSelect: func(i int) tea.Cmd { m.s.SetModel(avail[i]); m.note("model: " + avail[i].Key()); return nil },
		onSave: func(i int) tea.Cmd {
			m.s.SetModel(avail[i])
			if err := m.s.SaveDefaultModel(); err != nil {
				m.note("could not save: " + err.Error())
			} else {
				m.note("default model saved: " + avail[i].Key())
			}
			return nil
		}}
	return nil
}

func (m *model) openTree() tea.Cmd {
	entries := m.s.Store.BranchEntries()
	type item struct{ id, label string }
	var items []item
	for _, e := range entries {
		if e.Type == "message" && e.Message != nil && e.Message.Role != "toolResult" {
			label := fmt.Sprintf("%s  %-9s %s", e.ID, e.Message.Role, truncate(strings.ReplaceAll(e.Message.TextContent(), "\n", " "), 70))
			items = append(items, item{e.ID, label})
		}
	}
	if len(items) == 0 {
		m.note("nothing to go back to yet")
		return nil
	}
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.label
	}
	m.pick = &picker{title: "Go back to… (later messages stay in the file as another branch)", items: labels, idx: len(labels) - 1,
		onSelect: func(i int) tea.Cmd {
			if err := m.s.NavigateTree(items[i].id); err != nil {
				m.note(err.Error())
				return nil
			}
			m.rebuildFromSession()
			m.note("moved to " + items[i].id + "; new messages branch from here")
			return nil
		}}
	return nil
}

func (m *model) openFork() tea.Cmd {
	points := m.s.ForkPoints()
	if len(points) == 0 {
		m.note("no user messages to fork from")
		return nil
	}
	labels := make([]string, len(points))
	for i, p := range points {
		labels[i] = truncate(strings.ReplaceAll(p.Text, "\n", " "), 90)
	}
	m.pick = &picker{title: "Fork: pick the message to redo in a new session", items: labels, idx: len(labels) - 1,
		onSelect: func(i int) tea.Cmd {
			text, err := m.s.Fork(points[i].EntryID)
			if err != nil {
				m.note(err.Error())
				return nil
			}
			m.rebuildFromSession()
			m.ta.SetValue(text)
			m.growEditor()
			m.note("forked into a new session" + pathNote(m.s.Store.Path()) + "; edit the message and press Enter")
			return nil
		}}
	return nil
}

func (m *model) openResume() tea.Cmd {
	list := m.s.Sessions()
	if len(list) == 0 {
		m.note("no saved sessions for this folder")
		return nil
	}
	labels := make([]string, len(list))
	for i, info := range list {
		name := info.Name
		if name == "" {
			name = info.FirstUser
		}
		labels[i] = fmt.Sprintf("%s  %2d msgs  %s", info.Modified.Format("Jan 02 15:04"), info.Messages, truncate(name, 60))
	}
	m.pick = &picker{title: "Resume a session", items: labels, idx: 0,
		onSelect: func(i int) tea.Cmd {
			if err := m.s.SwitchSession(list[i].Path); err != nil {
				m.note(err.Error())
				return nil
			}
			m.rebuildFromSession()
			return nil
		}}
	return nil
}

func (m *model) openLogin(provider string) tea.Cmd {
	m.ta.Placeholder = "Paste the API key for " + provider + " and press Enter"
	m.pendingLogin = provider
	return nil
}

func (m *model) copyLast() {
	text := m.s.LastAssistantText()
	if text == "" {
		m.setStatus("nothing to copy")
		return
	}
	for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if cmd.Run() == nil {
				m.setStatus("copied")
				return
			}
		}
	}
	m.setStatus("no clipboard tool found (pbcopy, wl-copy, xclip)")
}

func (m *model) openExternalEditor() tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		m.setStatus("set $EDITOR to use Ctrl+G")
		return nil
	}
	f, err := os.CreateTemp("", "pig-prompt-*.md")
	if err != nil {
		return nil
	}
	f.WriteString(m.ta.Value())
	f.Close()
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return editorDoneMsg{path: f.Name(), err: err} })
}

// completePath fills in the file path under the cursor's word.
func (m *model) completePath() {
	v := m.ta.Value()
	if v == "" {
		return
	}
	i := strings.LastIndexAny(v, " \n")
	word := v[i+1:]
	if word == "" {
		return
	}
	word = strings.TrimPrefix(word, "@")
	pattern := word + "*"
	if !filepath.IsAbs(pattern) && !strings.HasPrefix(pattern, "~") {
		pattern = filepath.Join(m.s.Opts.Cwd, pattern)
	}
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return
	}
	// Common prefix of all matches.
	common := matches[0]
	for _, mt := range matches[1:] {
		for !strings.HasPrefix(mt, common) {
			common = common[:len(common)-1]
		}
	}
	rel := common
	if r, err := filepath.Rel(m.s.Opts.Cwd, common); err == nil && !strings.HasPrefix(r, "..") && !filepath.IsAbs(word) {
		rel = r
	}
	if len(matches) == 1 {
		if st, err := os.Stat(matches[0]); err == nil && st.IsDir() {
			rel += "/"
		}
	}
	m.ta.SetValue(v[:i+1] + rel)
	m.ta.CursorEnd()
}
