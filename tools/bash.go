package tools

// The tool description, JSON schema and prompt snippet are ported from pi:
// packages/coding-agent/src/core/tools/bash.ts
// Copyright (c) 2025 Mario Zechner, MIT licensed. See LICENSE.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tobyjackson/pig/ai"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Bash runs a shell command in cwd and returns its combined output.
type Bash struct {
	cwd   string
	shell string
	// Env adds variables to every command (session details, for example).
	Env map[string]string
}

// NewBash makes the bash tool. shell is the program run with -c; empty
// means bash.
func NewBash(cwd, shell string) *Bash {
	if shell == "" {
		shell = "bash"
	}
	return &Bash{cwd: cwd, shell: shell}
}

func (*Bash) Name() string { return "bash" }
func (*Bash) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", MaxLines, MaxBytes/1024)
}
func (*Bash) PromptSnippet() string { return "Execute bash commands (ls, grep, find, etc.)" }
func (*Bash) PromptGuidelines() []string {
	return []string{"You can inspect PIG_* environment variables for current model and session details."}
}
func (*Bash) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"command":{"type":"string","description":"Shell command to execute"},
"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},
"required":["command"],"additionalProperties":false}`)
}

// maxCapturedOutput bounds how much of a command's output is held in memory.
// Truncation already limits what the model sees, but a command like `yes`
// would otherwise grow the buffer until the process ran out of memory.
const maxCapturedOutput = 10 << 20

// truncationMarkerReserve is the room kept for the marker boundedBuffer adds,
// so String never returns more than the advertised limit.
const truncationMarkerReserve = 256

// boundedBuffer holds the start and the end of a stream, dropping the middle
// once it grows past the limit. Keeping both ends matters: a build log's
// failure is the last thing it prints, so a head-only cap would discard the
// one line worth reading.
type boundedBuffer struct {
	limit   int
	headCap int
	head    []byte
	tail    []byte
	total   int
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit, headCap: limit / 2}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	if remaining := b.headCap - len(b.head); remaining > 0 {
		keep := min(remaining, len(p))
		b.head = append(b.head, p[:keep]...)
		p = p[keep:]
	}
	if len(p) > 0 {
		tailCap := max(0, b.limit-b.headCap-truncationMarkerReserve)
		if len(p) >= tailCap {
			b.tail = append(b.tail[:0], p[len(p)-tailCap:]...)
		} else {
			b.tail = append(b.tail, p...)
			if len(b.tail) > tailCap {
				b.tail = append(b.tail[:0], b.tail[len(b.tail)-tailCap:]...)
			}
		}
	}
	return n, nil
}

// dropped reports whether anything was discarded. The marker lives in String,
// so callers check this rather than comparing lengths.
func (b *boundedBuffer) dropped() bool {
	return b.total > len(b.head)+len(b.tail)
}

func (b *boundedBuffer) String() string {
	if !b.dropped() {
		return string(b.head) + string(b.tail)
	}
	omitted := b.total - len(b.head) - len(b.tail)
	marker := fmt.Sprintf("\n\n[%s of output omitted: showing the first %s and the last %s]\n\n",
		FormatSize(omitted), FormatSize(len(b.head)), FormatSize(len(b.tail)))
	return string(b.head) + marker + string(b.tail)
}

// pipeQuietPeriod is how long Run keeps draining after the child exits before
// concluding that something else still holds the write end open. It only
// elapses on a command that leaves a background process behind; a normal
// command reaches EOF immediately.
const pipeQuietPeriod = 250 * time.Millisecond

// Run executes a command, streaming output to onData, and returns the exit
// code. A zero timeout means none. The whole process group is killed on
// cancel or timeout.
func (b *Bash) Run(ctx context.Context, command string, timeout time.Duration, onData func([]byte)) (int, error) {
	if _, err := os.Stat(b.cwd); err != nil {
		return -1, fmt.Errorf("Working directory does not exist: %s", b.cwd)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.Command(b.shell, "-c", command)
	cmd.Dir = b.cwd
	cmd.Env = os.Environ()
	for k, v := range b.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	newProcessGroup(cmd)
	cmd.Stdin = nil
	pr, pw, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return -1, err
	}
	pw.Close()
	var wg sync.WaitGroup
	progress := make(chan struct{}, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				select {
				case progress <- struct{}{}:
				default:
				}
				onData(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		killProcessGroup(cmd)
		waitErr = <-done
	}
	// The child exiting does not mean the pipe is empty: the kernel can still
	// hold a buffer of unread output, and closing the read end here would throw
	// it away, losing exactly the trailing lines that explain a failure. Give
	// the reader until it goes quiet. A background process that inherited the
	// write end holds the pipe open forever, so waiting for EOF unconditionally
	// would stall every `cmd &` instead.
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	quiet := time.NewTimer(pipeQuietPeriod)
	defer quiet.Stop()
	for draining := true; draining; {
		select {
		case <-drained:
			draining = false
		case <-progress:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(pipeQuietPeriod)
		case <-quiet.C:
			draining = false
		}
	}
	pr.Close()
	return b.finish(ctx, waitErr)
}

// finish maps a completed command to its exit status and error.
func (b *Bash) finish(ctx context.Context, waitErr error) (int, error) {
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		// A process killed by a signal has exit code -1, which says nothing.
		// Report the signal instead, so "exited with code -1" never reaches
		// the user or the model.
		if sig := signalName(ee); sig != "" {
			return -1, fmt.Errorf("Command was killed by %s", sig)
		}
		return ee.ExitCode(), nil
	}
	if waitErr != nil {
		return -1, waitErr
	}
	return 0, nil
}

func (b *Bash) Execute(ctx context.Context, _ string, args json.RawMessage, onUpdate func(Result)) Result {
	var in struct {
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Command == "" {
		return ErrorResult("bash: a command is required")
	}
	var mu sync.Mutex
	buf := newBoundedBuffer(maxCapturedOutput)
	last := time.Time{}
	code, err := b.Run(ctx, in.Command, time.Duration(in.Timeout*float64(time.Second)), func(p []byte) {
		mu.Lock()
		_, _ = buf.Write(p)
		emit := onUpdate != nil && time.Since(last) > 250*time.Millisecond
		var snapshot string
		if emit {
			last = time.Now()
			snapshot = buf.String()
		}
		mu.Unlock()
		if emit {
			onUpdate(TextResult(TruncateTail(snapshot).Content))
		}
	})
	output := buf.String()
	t := TruncateTail(output)
	text := t.Content
	var details map[string]any
	if t.Truncated {
		path := saveFullOutput(output)
		details = map[string]any{"truncation": t, "fullOutputPath": path}
		start := t.TotalLines - t.OutputLines + 1
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]", start, t.TotalLines, t.TotalLines, path)
	}
	if buf.dropped() {
		text += fmt.Sprintf("\n\n[Output exceeded %s; the middle was dropped and both ends kept.]", FormatSize(maxCapturedOutput))
	}
	status := func(s string) string {
		if text == "" {
			return s
		}
		return text + "\n\n" + s
	}
	switch {
	case err == context.DeadlineExceeded:
		return ErrorResult(status(fmt.Sprintf("Command timed out after %g seconds", in.Timeout)))
	case err == context.Canceled:
		return ErrorResult(status("Command aborted"))
	case err != nil:
		return ErrorResult(status(err.Error()))
	case code != 0:
		return ErrorResult(status(fmt.Sprintf("Command exited with code %d", code)))
	}
	if text == "" {
		text = "(no output)"
	}
	return Result{Content: []ai.Content{ai.Text(text)}, Details: details}
}

func saveFullOutput(s string) string {
	f, err := os.CreateTemp("", "pig-bash-*.txt")
	if err != nil {
		return "(could not save)"
	}
	defer f.Close()
	_, _ = f.WriteString(s)
	return f.Name()
}
