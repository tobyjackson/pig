//go:build windows

package tools

import "os/exec"

// signalName is Unix-only. On Windows a killed process reports a large exit
// code rather than a signal, so there is nothing to name here.
func signalName(*exec.ExitError) string { return "" }
