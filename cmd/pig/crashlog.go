package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/tobyjackson/pig/resources"
)

// installCrashLog sends a copy of any fatal error or panic to a file under
// pig's home before the process dies. A fatal runtime error, such as a data
// race or a stack overflow, cannot be recovered: the terminal scrolls away
// with the report and it is lost. The file keeps it. The returned func
// removes the file on a normal exit; a crash never returns, so a crash log is
// the only thing left behind.
func installCrashLog(args []string) func() {
	dir := filepath.Join(resources.GlobalDir(), "crashes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}
	}
	name := time.Now().UTC().Format("2006-01-02T15-04-05") + "-" + strconv.Itoa(os.Getpid()) + ".log"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return func() {}
	}
	// The header goes in first so it sits above the traceback the runtime
	// appends. The fatal error's own one-line reason is printed before the
	// runtime starts recording, so it stays on stderr; the traceback below
	// names the failing frames.
	cwd, _ := os.Getwd()
	fmt.Fprintf(f, "pig %s\ncwd: %s\nargs: %v\n\n", version, cwd, args)
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		os.Remove(path)
		return func() {}
	}
	f.Close() // SetCrashOutput keeps its own copy of the descriptor
	return func() { os.Remove(path) }
}
