//go:build unix

package hooks

import (
	"os/exec"
	"syscall"
)

// ownGroup runs c in its own process group and kills the whole group on
// timeout, so a hook's children do not outlive it.
func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
}
