package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// A single line longer than the byte limit is cut mid-line, and the cut must
// not land inside a multi-byte rune: the model would receive U+FFFD.
func TestTruncateTailSplitsOnRuneBoundary(t *testing.T) {
	line := strings.Repeat("\u20ac", 20000) // 60000 bytes of 3-byte runes
	tr := TruncateTail(line)
	if !tr.LastLinePartial {
		t.Fatal("expected a partial line")
	}
	if !utf8.ValidString(tr.Content) {
		t.Fatalf("cut split a rune: first bytes %q", tr.Content[:min(6, len(tr.Content))])
	}
	if len(tr.Content) > MaxBytes {
		t.Fatalf("kept %d bytes, over the %d limit", len(tr.Content), MaxBytes)
	}
}

// A line that was never valid UTF-8 must not be stripped to nothing: the
// boundary search gives up after utf8.UTFMax-1 bytes.
func TestTruncateTailKeepsBinaryLine(t *testing.T) {
	line := strings.Repeat("\xff", 60000)
	tr := TruncateTail(line)
	if len(tr.Content) < MaxBytes-(utf8.UTFMax-1) {
		t.Fatalf("binary line was stripped: got %d, want about %d", len(tr.Content), MaxBytes)
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

// A command that floods stdout must not grow the captured buffer without
// bound. The cap is generous, so this checks the mechanism, not the constant.
func TestBashOutputIsCapped(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	// yes prints forever; head limits it to well over the cap.
	res := b.Execute(context.Background(), "1", json.RawMessage(`{"command":"yes x | head -c 12000000","timeout":30}`), nil)
	if !strings.Contains(res.Content[0].Text, "the middle was dropped") {
		t.Fatalf("flooded output should report the cap: %q", res.Content[0].Text[:min(200, len(res.Content[0].Text))])
	}
}

// The point of keeping both ends: a command whose useful output is its last
// line still shows that line after the cap fires. A head-only cap loses it.
func TestBashCapKeepsTail(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	cmd := `echo FIRST_LINE; yes filler | head -c 12000000; echo LAST_LINE`
	res := b.Execute(context.Background(), "1", json.RawMessage(`{"command":`+strconv.Quote(cmd)+`,"timeout":30}`), nil)
	got := res.Content[0].Text
	if !strings.Contains(got, "LAST_LINE") {
		t.Fatalf("the last line was lost: %q", got[max(0, len(got)-200):])
	}
	if !strings.Contains(got, "the middle was dropped") {
		t.Fatal("the drop was not reported")
	}
}

// The child exiting does not mean the pipe is empty. Output still sitting in
// the kernel buffer must reach the caller, or a failing command's last lines
// are lost.
func TestBashKeepsOutputAfterExit(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	var sb strings.Builder
	code, err := b.Run(context.Background(), `echo FIRST_LINE; yes filler | head -c 12000000; echo LAST_LINE`, 0, func(p []byte) { sb.Write(p) })
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	got := sb.String()
	if !strings.Contains(got, "LAST_LINE") {
		t.Fatalf("trailing output lost: %d bytes captured", len(got))
	}
	if !strings.Contains(got, "FIRST_LINE") {
		t.Fatal("leading output lost")
	}
}

// A background process inherits the write end, so the pipe never reaches EOF.
// Waiting for it would stall every `cmd &` on the quiet period.
func TestBashDoesNotWaitForBackgroundHolder(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	start := time.Now()
	var sb strings.Builder
	if _, err := b.Run(context.Background(), `sleep 30 & echo done`, 0, func(p []byte) { sb.Write(p) }); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("waited %s on a background holder", took)
	}
	if !strings.Contains(sb.String(), "done") {
		t.Fatalf("lost output: %q", sb.String())
	}
}

// Output that keeps arriving must not be cut short by the quiet timer.
func TestBashKeepsSlowOutput(t *testing.T) {
	b := NewBash(t.TempDir(), "")
	var sb strings.Builder
	cmd := `for i in 1 2 3 4 5; do echo "chunk$i"; sleep 0.1; done`
	if _, err := b.Run(context.Background(), cmd, 0, func(p []byte) { sb.Write(p) }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chunk1", "chunk3", "chunk5"} {
		if !strings.Contains(sb.String(), want) {
			t.Fatalf("lost %s: %q", want, sb.String())
		}
	}
}

// A killed command leaves the file untouched, so a failed edit cannot truncate
// the original.
func TestEditLeavesFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("original"), 0o644)
	e := NewEdit(dir)
	res := e.Execute(context.Background(), "1", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"absent","newText":"x"}]}`), nil)
	if !res.IsError {
		t.Fatal("editing text that is not there should fail")
	}
	if data, _ := os.ReadFile(p); string(data) != "original" {
		t.Fatalf("file changed after a failed edit: %q", data)
	}
}

// A successful write leaves no temp files behind.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	if res := w.Execute(context.Background(), "1", json.RawMessage(`{"path":"f.txt","content":"hi"}`), nil); res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pig-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
