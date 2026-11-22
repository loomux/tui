//go:build !unix

package browser

import "os/exec"

// detach does nothing here: Command opens no browser outside Unix.
func detach(*exec.Cmd) {}
