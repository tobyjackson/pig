package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTruncateHeadLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString("line\n")
	}
	tr := TruncateHead(sb.String())
	if !tr.Truncated || tr.TruncatedBy != "lines" || tr.OutputLines != MaxLines {
		t.Fatalf("got %+v", tr)
	}
}

func TestTruncateTailBytes(t *testing.T) {
	line := strings.Repeat("x", 1000) + "\n"
	s := strings.Repeat(line, 100) // 100KB, 100 lines
	tr := TruncateTail(s)
	if !tr.Truncated || tr.TruncatedBy != "bytes" || tr.OutputBytes > MaxBytes {
		t.Fatalf("got truncated=%v by=%s bytes=%d", tr.Truncated, tr.TruncatedBy, tr.OutputBytes)
	}
	if !strings.HasSuffix(tr.Content, strings.Repeat("x", 1000)) {
		t.Fatal("tail should keep the last line whole")
	}
}

func TestApplyEdits(t *testing.T) {
	out, err := ApplyEdits("a\nb\nc\n", []Replacement{{"b", "B"}, {"c", "C"}}, "f")
	if err != nil || out != "a\nB\nC\n" {
		t.Fatalf("got %q %v", out, err)
	}
	if _, err := ApplyEdits("a a", []Replacement{{"a", "b"}}, "f"); err == nil {
		t.Fatal("ambiguous match should fail")
	}
	if _, err := ApplyEdits("abc", []Replacement{{"ab", "x"}, {"bc", "y"}}, "f"); err == nil {
		t.Fatal("overlap should fail")
	}
	if _, err := ApplyEdits("abc", []Replacement{{"zzz", "x"}}, "f"); err == nil {
		t.Fatal("missing text should fail")
	}
}

func TestEditToolLegacyArgs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("hello world\r\n"), 0o644)
	e := NewEdit(dir)
	res := e.Execute(context.Background(), "1", json.RawMessage(`{"path":"f.txt","oldText":"world","newText":"pig"}`), nil)
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "hello pig\r\n" {
		t.Fatalf("CRLF not preserved: %q", data)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("1\n2\n3\n4\n5\n"), 0o644)
	r := NewRead(dir)
	res := r.Execute(context.Background(), "1", json.RawMessage(`{"path":"f.txt","offset":2,"limit":2}`), nil)
	text := res.Content[0].Text
	if !strings.HasPrefix(text, "2\n3") || !strings.Contains(text, "offset=4") {
		t.Fatalf("got %q", text)
	}
}

func TestWriteCreatesDirs(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	res := w.Execute(context.Background(), "1", json.RawMessage(`{"path":"a/b/c.txt","content":"hi"}`), nil)
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a/b/c.txt")); string(data) != "hi" {
		t.Fatal("file not written")
	}
}

func TestBashExitCodeAndTimeout(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	res := b.Execute(context.Background(), "1", json.RawMessage(`{"command":"echo out; exit 3"}`), nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "exited with code 3") || !strings.Contains(res.Content[0].Text, "out") {
		t.Fatalf("got %+v", res)
	}
	res = b.Execute(context.Background(), "1", json.RawMessage(`{"command":"sleep 5","timeout":0.2}`), nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "timed out") {
		t.Fatalf("got %+v", res)
	}
}

// A process killed by a signal reports exit code -1, which says nothing. The
// message must name the signal instead.
func TestBashSignalReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals are Unix-only")
	}
	b := NewBash(t.TempDir(), "")
	res := b.Execute(context.Background(), "1", json.RawMessage(`{"command":"kill -TERM $$"}`), nil)
	if !res.IsError {
		t.Fatalf("a killed command should be an error: %+v", res)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "SIGTERM") {
		t.Fatalf("signal not named: %q", got)
	}
	if strings.Contains(got, "code -1") {
		t.Fatalf("unhelpful exit code leaked through: %q", got)
	}
}

func TestDiff(t *testing.T) {
	d := Diff("a\nb\nc", "a\nB\nc")
	if !strings.Contains(d, "- b") || !strings.Contains(d, "+ B") {
		t.Fatalf("got %q", d)
	}
}
