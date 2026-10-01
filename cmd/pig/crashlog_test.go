package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// TestCrashLogWritten re-runs this test binary as a helper that installs the
// crash log and then panics, and checks that a report was left in
// PIG_DIR/crashes. A panic stands in for the fatal runtime errors, such as the
// map race that motivated this, which cannot be recovered but are written to
// the same file.
func TestCrashLogWritten(t *testing.T) {
	if os.Getenv("PIG_CRASH_HELPER") == "1" {
		installCrashLog([]string{"helper"})
		panic("boom")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCrashLogWritten")
	cmd.Env = append(os.Environ(), "PIG_CRASH_HELPER=1", "PIG_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("helper did not crash: %s", out)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "crashes", "*.log"))
	if len(matches) != 1 {
		t.Fatalf("want one crash log, got %v", matches)
	}
	data, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(data), "panic: boom") {
		t.Fatalf("crash log has no traceback:\n%s", data)
	}
}

// TestCrashLogRemovedOnCleanExit checks a normal run leaves nothing behind.
func TestCrashLogRemovedOnCleanExit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIG_DIR", dir)
	clean := installCrashLog(nil)
	clean()
	debug.SetCrashOutput(nil, debug.CrashOptions{})
	if matches, _ := filepath.Glob(filepath.Join(dir, "crashes", "*.log")); len(matches) != 0 {
		t.Fatalf("clean exit left %v", matches)
	}
}
