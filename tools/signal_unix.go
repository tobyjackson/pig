//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// signalName returns the name of the signal that killed the process, or "" if
// it exited on its own. A signal death reports exit code -1, so the signal is
// the only thing worth showing.
func signalName(ee *exec.ExitError) string {
	ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	switch ws.Signal() {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGXCPU:
		return "SIGXCPU"
	case syscall.SIGXFSZ:
		return "SIGXFSZ"
	default:
		return ws.Signal().String()
	}
}
