package extensions

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHelloExtension(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	path, _ := filepath.Abs("../examples/extensions/hello.py")
	var notices []Notice
	m := Load(context.Background(), []string{path}, t.TempDir(), func(n Notice) { notices = append(notices, n) })
	defer m.Close()
	if len(m.Extensions) != 1 || m.Extensions[0].Ready.Name != "hello" {
		t.Fatalf("extensions: %+v notices=%+v", m.Extensions, notices)
	}
	ts := m.Tools()
	if len(ts) != 1 || ts[0].Name() != "shout" {
		t.Fatalf("tools: %d", len(ts))
	}
	res := ts[0].Execute(context.Background(), "1", json.RawMessage(`{"text":"abc"}`), nil)
	if res.IsError || res.Content[0].Text != "ABC" {
		t.Fatalf("tool result: %+v", res)
	}
	reply := m.Emit("tool_call", map[string]any{"toolName": "bash", "toolCallId": "x", "input": map[string]any{"command": "rm -rf /"}})
	if string(reply["block"]) != "true" {
		t.Fatalf("expected block, got %v", reply)
	}
	reply = m.Emit("tool_call", map[string]any{"toolName": "bash", "toolCallId": "x", "input": map[string]any{"command": "ls"}})
	if _, blocked := reply["block"]; blocked {
		t.Fatal("ls should not be blocked")
	}
	cr, ok, err := m.RunCommand("hello", "world")
	if !ok || err != nil || cr.Notify == "" {
		t.Fatalf("command: %v %v %+v", ok, err, cr)
	}
	m.Emit("session_start", map[string]any{"reason": "startup"})
	found := false
	for _, n := range notices {
		if n.Message == "hello extension loaded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notify not received: %+v", notices)
	}
}

// A message queued while the agent is busy must still pass the input hooks.
// Before this, every steer and follow-up went straight to the agent queue and
// an extension could never see or rewrite it.
func TestInputEventRewritesAndBlocks(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	path, _ := filepath.Abs("../examples/extensions/hello.py")
	m := Load(context.Background(), []string{path}, t.TempDir(), nil)
	defer m.Close()

	reply := m.Emit("input", map[string]any{"text": "? why is the sky blue", "queued": true})
	if string(reply["text"]) != `"why is the sky blue"` {
		t.Fatalf("text not rewritten: %v", reply)
	}
	reply = m.Emit("input", map[string]any{"text": "!drop", "queued": true})
	if string(reply["block"]) != "true" {
		t.Fatalf("expected block, got %v", reply)
	}
	reply = m.Emit("input", map[string]any{"text": "keep me", "queued": false})
	if _, blocked := reply["block"]; blocked {
		t.Fatalf("plain text should pass: %v", reply)
	}
}
