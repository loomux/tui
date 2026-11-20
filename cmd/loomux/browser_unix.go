//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach gives the browser command a session of its own. Ctrl+c at the
// terminal, which cancels the login, goes to every process in the
// terminal's foreground group: without this it would also end a browser
// the command had just started.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
