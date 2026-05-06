//go:build windows

package runlocal

import "os/exec"

// setProcessGroup is a no-op on Windows. Windows uses job objects
// for process trees, and exec.CommandContext's default cancellation
// (which calls os.Process.Kill on the immediate child) is sufficient
// for the simple shell-out cases this package supports. The
// Setpgid/SIGKILL technique used on Unix is not available here.
func setProcessGroup(_ *exec.Cmd) {}
