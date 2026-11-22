// Package browser opens a web link in the user's browser: a
// conversation on the web (the list's o), and later the sign-in link.
//
// It opens one only where the browser would appear in front of the user:
// on Linux and the BSDs when there is a display (X or Wayland, forwarded
// over ssh included), and on macOS when the session isn't over ssh (open
// would show it on the Mac's own screen). Elsewhere it is ErrNoBrowser,
// and the caller shows the link to copy instead. Windows isn't supported
// (the design defers it).
package browser

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"runtime"
)

// ErrNoBrowser is Open where no browser can be shown to the user.
var ErrNoBrowser = errors.New("no browser to open here")

// Open opens link (an http or https URL) in the user's browser. It
// doesn't wait for the browser: an error is one starting it.
func Open(link string) error {
	return open(runtime.GOOS, os.Getenv, link, start)
}

func open(goos string, getenv func(string) string, link string, start func([]string) error) error {
	argv := Command(goos, getenv, link)
	if argv == nil {
		return ErrNoBrowser
	}
	return start(argv)
}

// Command is the command that opens link in a browser on goos, with the
// environment getenv, or nil when there is none to run: no display, an
// ssh session to a Mac, a system it can't open one on, or a link that
// isn't an absolute http or https URL.
func Command(goos string, getenv func(string) string, link string) []string {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil
	}
	switch goos {
	case "darwin":
		if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
			return nil
		}
		return []string{"open", link}
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		return []string{"xdg-open", link}
	}
	return nil
}

// start runs argv without a terminal: no input, its output discarded,
// and (on Unix) in a session of its own, so a browser that falls back to
// a text browser can't take over the screen. It doesn't wait for the
// command, and reaps it when it ends.
func start(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
