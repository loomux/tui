//go:build !linux

package attach

import "syscall"

// sysProcAttr: only Linux can hang a command up when its parent dies.
// Elsewhere the hold's expiry is the guarantee: the server detaches the
// session's clients (the design's "Crashed TUI").
func sysProcAttr() *syscall.SysProcAttr { return nil }
