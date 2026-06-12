//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
)

// configureProcessTree makes the command lead its own process group and
// arranges for cancellation to kill the WHOLE group. Without this, the
// context kills only /bin/sh: a backgrounded grandchild (a worker
// smoke-testing a server it just built) survives, keeps the output pipe
// open, and CombinedOutput blocks forever — wedging the worker session past
// any timeout and leaking a process that squats on its port.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid signals the entire process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
