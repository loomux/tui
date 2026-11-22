//go:build unix

package browser

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, without a controlling
// terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
