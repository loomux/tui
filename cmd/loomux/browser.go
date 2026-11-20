package main

import (
	"os"
	"os/exec"
	"runtime"
)

// browserCommand is the command that opens a URL in the user's browser
// on goos, or "" where there is none to try: Windows (not supported
// yet), and a Linux or BSD session without a display, such as a terminal
// on a remote machine. There xdg-open would start a text browser in this
// terminal, and the printed link is what the user needs.
func browserCommand(goos string, getenv func(string) string) string {
	switch goos {
	case "darwin":
		return "open"
	case "windows":
		return ""
	}
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return ""
	}
	return "xdg-open"
}

// startBrowser opens link in the user's browser and reports whether it
// started the command for it. It doesn't wait for the browser, which
// outlives the login, and the command's own output is dropped rather
// than mixed into the terminal's.
//
// link is the only argument, and no shell is involved. It is the link
// `loomux login` prints, which holds the token's hash and never the
// token.
func startBrowser(link string) bool {
	name := browserCommand(runtime.GOOS, os.Getenv)
	if name == "" {
		return false
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	cmd := exec.Command(path, link)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}
