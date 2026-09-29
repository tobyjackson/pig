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
	"strings"
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

// NewBash makes the bash tool. shell may be empty to use $SHELL or bash.
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
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
	pr.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
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
	var sb strings.Builder
	last := time.Time{}
	code, err := b.Run(ctx, in.Command, time.Duration(in.Timeout*float64(time.Second)), func(p []byte) {
		mu.Lock()
		sb.Write(p)
		snapshot := sb.String()
		mu.Unlock()
		if onUpdate != nil && time.Since(last) > 250*time.Millisecond {
			last = time.Now()
			onUpdate(TextResult(TruncateTail(snapshot).Content))
		}
	})
	output := sb.String()
	t := TruncateTail(output)
	text := t.Content
	var details map[string]any
	if t.Truncated {
		path := saveFullOutput(output)
		details = map[string]any{"truncation": t, "fullOutputPath": path}
		start := t.TotalLines - t.OutputLines + 1
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]", start, t.TotalLines, t.TotalLines, path)
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
