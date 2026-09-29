//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// newProcessGroup puts the command in its own process group, so a cancel or
// timeout kills the shell and everything it started, not just the shell.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the whole group. The negative pid is the group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
