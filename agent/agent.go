// Package agent runs the loop: send the conversation to the model, run any
// tools it asks for, feed the results back, repeat until the model stops.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/tools"
)

// Event is what the agent reports while it works. Types:
// agent_start, agent_end, turn_start, turn_end, message_start,
// message_update, message_end, tool_execution_start,
// tool_execution_update, tool_execution_end, queue_update, auto_retry.
type Event struct {
	Type        string          `json:"type"`
	Message     *ai.Message     `json:"message,omitempty"`
	Messages    []ai.Message    `json:"messages,omitempty"`
	Update      *ai.Event       `json:"assistantMessageEvent,omitempty"`
	ToolCallID  string          `json:"toolCallId,omitempty"`
	ToolName    string          `json:"toolName,omitempty"`
	Args        json.RawMessage `json:"args,omitempty"`
	Result      *tools.Result   `json:"result,omitempty"`
	Partial     *tools.Result   `json:"partialResult,omitempty"`
	IsError     bool            `json:"isError,omitempty"`
	ToolResults []ai.Message    `json:"toolResults,omitempty"`
	Steering    []string        `json:"steering,omitempty"`
	FollowUp    []string        `json:"followUp,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
	MaxAttempts int             `json:"maxAttempts,omitempty"`
	DelayMs     int64           `json:"delayMs,omitempty"`
	Error       string          `json:"error,omitempty"`
	// Used by runtime-level events (compaction, notices, retries).
	Reason  string `json:"reason,omitempty"`
	Summary string `json:"summary,omitempty"`
	Level   string `json:"level,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Hooks let the host (extensions, compaction) step in at key points.
// Every hook is optional.
type Hooks struct {
	// BeforeTurn runs before each model call; return an error to stop.
	BeforeTurn func(ctx context.Context) error
	// TransformContext may edit the messages sent to the model.
	TransformContext func(msgs []ai.Message) []ai.Message
	// ToolCall may change args or block the call (returns a reason).
	ToolCall func(name, callID string, args json.RawMessage) (json.RawMessage, string)
	// ToolResult may replace a tool's result before it is recorded.
	ToolResult func(name, callID string, args json.RawMessage, r tools.Result) tools.Result
	// OnMessage is called for every message added to the history.
	OnMessage func(m ai.Message)
}

// Agent holds the state for one conversation.
type Agent struct {
	mu            sync.Mutex
	Model         ai.Model
	ThinkingLevel string
	SystemPrompt  string
	Tools         []tools.Tool
	Stream        ai.StreamFunc
	APIKey        func(provider string) string
	Hooks         Hooks
	// "one-at-a-time" (default) delivers one queued message per turn; "all"
	// delivers every queued message at once.
	SteeringMode string
	FollowUpMode string

	messages  []ai.Message
	steering  []ai.Message
	followUp  []ai.Message
	listeners []func(Event)
	running   bool
	cancel    context.CancelFunc
	idle      chan struct{}
}

// New makes an agent with the default streaming function.
func New() *Agent {
	return &Agent{Stream: ai.Stream, idle: closedChan()}
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Subscribe adds a listener. It returns a function that removes it.
func (a *Agent) Subscribe(fn func(Event)) func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listeners = append(a.listeners, fn)
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, l := range a.listeners {
			if fmt.Sprintf("%p", l) == fmt.Sprintf("%p", fn) {
				a.listeners = append(a.listeners[:i], a.listeners[i+1:]...)
				return
			}
		}
	}
}

func (a *Agent) emit(e Event) {
	a.mu.Lock()
	ls := append([]func(Event){}, a.listeners...)
	a.mu.Unlock()
	for _, l := range ls {
		l(e)
	}
}

// Messages returns a copy of the history.
func (a *Agent) Messages() []ai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ai.Message{}, a.messages...)
}

// SetMessages replaces the history (used when loading a session).
func (a *Agent) SetMessages(m []ai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append([]ai.Message{}, m...)
}

// IsRunning reports whether a prompt is being processed.
func (a *Agent) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// WaitForIdle blocks until the current run ends.
func (a *Agent) WaitForIdle() {
	a.mu.Lock()
	ch := a.idle
	a.mu.Unlock()
	<-ch
}

// Abort stops the current run. Queued messages are kept.
func (a *Agent) Abort() {
	a.mu.Lock()
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
}

// Steer queues a message delivered after the current tool calls finish.
func (a *Agent) Steer(m ai.Message) {
	a.mu.Lock()
	a.steering = append(a.steering, m)
	a.mu.Unlock()
	a.emitQueue()
}

// FollowUp queues a message delivered once the agent has nothing left to do.
func (a *Agent) FollowUp(m ai.Message) {
	a.mu.Lock()
	a.followUp = append(a.followUp, m)
	a.mu.Unlock()
	a.emitQueue()
}

// ClearQueue drops queued messages and returns them.
func (a *Agent) ClearQueue() (steering, followUp []ai.Message) {
	a.mu.Lock()
	steering, followUp = a.steering, a.followUp
	a.steering, a.followUp = nil, nil
	a.mu.Unlock()
	a.emitQueue()
	return
}

// QueueTexts returns the text of queued messages, for display.
func (a *Agent) QueueTexts() (steering, followUp []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.steering {
		steering = append(steering, m.TextContent())
	}
	for _, m := range a.followUp {
		followUp = append(followUp, m.TextContent())
	}
	return
}

func (a *Agent) emitQueue() {
	s, f := a.QueueTexts()
	a.emit(Event{Type: "queue_update", Steering: s, FollowUp: f})
}

func (a *Agent) popSteering() []ai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steering) == 0 {
		return nil
	}
	if a.SteeringMode == "all" {
		out := a.steering
		a.steering = nil
		return out
	}
	m := a.steering[0]
	a.steering = a.steering[1:]
	return []ai.Message{m}
}

func (a *Agent) popFollowUp() []ai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.followUp) == 0 {
		return nil
	}
	if a.FollowUpMode == "all" {
		out := a.followUp
		a.followUp = nil
		return out
	}
	m := a.followUp[0]
	a.followUp = a.followUp[1:]
	return []ai.Message{m}
}

func (a *Agent) addMessage(m ai.Message) {
	a.mu.Lock()
	a.messages = append(a.messages, m)
	a.mu.Unlock()
	if a.Hooks.OnMessage != nil {
		a.Hooks.OnMessage(m)
	}
}

// Prompt adds a user message and runs the loop until the model is done.
// It blocks; use Subscribe to watch progress. If the agent is already
// running, the message is queued as steering instead.
func (a *Agent) Prompt(ctx context.Context, m ai.Message) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		a.Steer(m)
		return nil
	}
	a.running = true
	a.idle = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.running = false
		a.cancel = nil
		close(a.idle)
		a.mu.Unlock()
	}()

	a.addMessage(m)
	a.emit(Event{Type: "agent_start"})
	start := len(a.messages) - 1
	err := a.loop(runCtx)
	a.emit(Event{Type: "agent_end", Messages: a.Messages()[start:]})
	return err
}

// Continue runs the loop without adding a message (e.g. after a manual edit).
func (a *Agent) Continue(ctx context.Context) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("agent is already running")
	}
	a.running = true
	a.idle = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.running = false
		a.cancel = nil
		close(a.idle)
		a.mu.Unlock()
	}()
	a.emit(Event{Type: "agent_start"})
	start := len(a.messages)
	err := a.loop(runCtx)
	a.emit(Event{Type: "agent_end", Messages: a.Messages()[start:]})
	return err
}

func (a *Agent) loop(ctx context.Context) error {
	for {
		if a.Hooks.BeforeTurn != nil {
			if err := a.Hooks.BeforeTurn(ctx); err != nil {
				return err
			}
		}
		a.emit(Event{Type: "turn_start"})
		msg := a.streamAssistant(ctx)
		a.addMessage(msg)
		a.emit(Event{Type: "message_end", Message: &msg})

		if msg.StopReason == ai.StopError || msg.StopReason == ai.StopAborted {
			a.emit(Event{Type: "turn_end", Message: &msg})
			if msg.StopReason == ai.StopAborted {
				return context.Canceled
			}
			return fmt.Errorf("%s", msg.ErrorMessage)
		}

		var results []ai.Message
		for _, call := range msg.ToolCalls() {
			if ctx.Err() != nil {
				break
			}
			results = append(results, a.runTool(ctx, call))
		}
		a.emit(Event{Type: "turn_end", Message: &msg, ToolResults: results})

		if len(msg.ToolCalls()) > 0 {
			// Steering messages ride along with the tool results.
			if s := a.popSteering(); len(s) > 0 {
				for _, m := range s {
					a.addMessage(m)
				}
				a.emitQueue()
			}
			if ctx.Err() != nil {
				return context.Canceled
			}
			continue
		}
		if s := a.popSteering(); len(s) > 0 {
			for _, m := range s {
				a.addMessage(m)
			}
			a.emitQueue()
			continue
		}
		if f := a.popFollowUp(); len(f) > 0 {
			for _, m := range f {
				a.addMessage(m)
			}
			a.emitQueue()
			continue
		}
		return nil
	}
}

func (a *Agent) streamAssistant(ctx context.Context) ai.Message {
	msgs := a.Messages()
	if a.Hooks.TransformContext != nil {
		msgs = a.Hooks.TransformContext(msgs)
	}
	defs := make([]ai.Tool, 0, len(a.Tools))
	for _, t := range a.Tools {
		defs = append(defs, tools.Definition(t))
	}
	key := ""
	if a.APIKey != nil {
		key = a.APIKey(a.Model.Provider)
	}
	opts := ai.Options{
		APIKey: key, ThinkingLevel: a.ThinkingLevel,
		OnRetry: func(attempt, max int, delay time.Duration, err error) {
			a.emit(Event{Type: "auto_retry", Attempt: attempt, MaxAttempts: max, DelayMs: delay.Milliseconds(), Error: err.Error()})
		},
	}
	stream := a.Stream(ctx, a.Model, ai.Context{SystemPrompt: a.SystemPrompt, Messages: msgs, Tools: defs}, opts)
	var final *ai.Message
	for ev := range stream {
		switch ev.Type {
		case "start":
			a.emit(Event{Type: "message_start", Message: ev.Partial})
		case "done":
			final = ev.Message
		case "error":
			final = ev.Message
		default:
			e := ev
			a.emit(Event{Type: "message_update", Message: ev.Partial, Update: &e})
		}
	}
	if final == nil {
		final = &ai.Message{Role: "assistant", Provider: a.Model.Provider, Model: a.Model.ID,
			Usage: &ai.Usage{}, StopReason: ai.StopError, ErrorMessage: "provider returned nothing", Timestamp: ai.Now()}
	}
	if ctx.Err() != nil && final.StopReason != ai.StopAborted {
		final.StopReason = ai.StopAborted
		if final.ErrorMessage == "" {
			final.ErrorMessage = "aborted"
		}
	}
	return *final
}

func (a *Agent) findTool(name string) tools.Tool {
	for _, t := range a.Tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func (a *Agent) runTool(ctx context.Context, call ai.Content) ai.Message {
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var result tools.Result
	if a.Hooks.ToolCall != nil {
		newArgs, block := a.Hooks.ToolCall(call.Name, call.ID, args)
		if block != "" {
			result = tools.ErrorResult("Tool call blocked: " + block)
			a.emit(Event{Type: "tool_execution_start", ToolCallID: call.ID, ToolName: call.Name, Args: args})
			return a.finishTool(call, args, result)
		}
		if len(newArgs) > 0 {
			args = newArgs
		}
	}
	a.emit(Event{Type: "tool_execution_start", ToolCallID: call.ID, ToolName: call.Name, Args: args})
	t := a.findTool(call.Name)
	if t == nil {
		result = tools.ErrorResult(fmt.Sprintf("Unknown tool: %s", call.Name))
	} else {
		result = t.Execute(ctx, call.ID, args, func(p tools.Result) {
			pp := p
			a.emit(Event{Type: "tool_execution_update", ToolCallID: call.ID, ToolName: call.Name, Args: args, Partial: &pp})
		})
		if ctx.Err() != nil && !result.IsError {
			result = tools.ErrorResult("Tool aborted")
		}
	}
	return a.finishTool(call, args, result)
}

func (a *Agent) finishTool(call ai.Content, args json.RawMessage, result tools.Result) ai.Message {
	if a.Hooks.ToolResult != nil {
		result = a.Hooks.ToolResult(call.Name, call.ID, args, result)
	}
	a.emit(Event{Type: "tool_execution_end", ToolCallID: call.ID, ToolName: call.Name, Result: &result, IsError: result.IsError})
	var details json.RawMessage
	if result.Details != nil {
		details, _ = json.Marshal(result.Details)
	}
	msg := ai.Message{Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name,
		Content: result.Content, Details: details, IsError: result.IsError, Timestamp: ai.Now()}
	if len(msg.Content) == 0 {
		msg.Content = []ai.Content{ai.Text("(no output)")}
	}
	a.addMessage(msg)
	return msg
}
