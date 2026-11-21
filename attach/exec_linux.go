package attach

import "syscall"

// sysProcAttr gives the attach command a hangup when the client dies
// (the design's "Crashed TUI": belt and braces beside the hold's
// expiry, which detaches it from the server's side). A tmux client
// detaches on it, and ssh closes its session.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGHUP}
}
