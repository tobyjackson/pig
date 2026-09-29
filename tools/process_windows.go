//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// newProcessGroup puts the command in its own process group on Windows too,
// using CREATE_NEW_PROCESS_GROUP. There is no job object here, so killing the
// group reaches the shell and its direct children; a grandchild that starts a
// group of its own may survive. Good enough for a coding agent's tool.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup kills the command and, best effort, its children. The
// negative pid form doesn't exist here, so it uses taskkill, which reaches the
// tree, and falls back to killing the process itself.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID",
		strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
