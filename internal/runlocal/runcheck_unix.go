//go:build !windows

package runlocal

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the spawned shell in its own process group
// and installs a Cancel func that sends SIGKILL to the entire group.
// This ensures any child processes the shell spawned (e.g. the actual
// test/build binary) are killed when the context is cancelled, not
// just the shell itself.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
