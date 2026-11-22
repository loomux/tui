package browser

import (
	"errors"
	"slices"
	"testing"
	"time"
)

const link = "https://loomux.example.net/conversations/c-1"

func env(vars ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(vars); i += 2 {
		m[vars[i]] = vars[i+1]
	}
	return func(k string) string { return m[k] }
}

// The command that opens a link: xdg-open where there is a display to
// open it on, open on macOS at its own screen, and nothing where a
// browser would open somewhere the user can't see it.
func TestCommand(t *testing.T) {
	for _, c := range []struct {
		name   string
		goos   string
		getenv func(string) string
		url    string
		want   []string
	}{
		{"linux, X", "linux", env("DISPLAY", ":0"), link, []string{"xdg-open", link}},
		{"linux, Wayland", "linux", env("WAYLAND_DISPLAY", "wayland-0"), link, []string{"xdg-open", link}},
		{"linux, X forwarded over ssh", "linux", env("DISPLAY", "localhost:10.0", "SSH_CONNECTION", "x"), link, []string{"xdg-open", link}},
		{"linux, no display (ssh, a console)", "linux", env("SSH_CONNECTION", "x"), link, nil},
		{"freebsd, X", "freebsd", env("DISPLAY", ":0"), link, []string{"xdg-open", link}},
		{"openbsd, no display", "openbsd", env(), link, nil},
		{"macOS", "darwin", env(), link, []string{"open", link}},
		{"macOS over ssh: the Mac's screen isn't the user's", "darwin", env("SSH_CONNECTION", "x"), link, nil},
		{"macOS over ssh, a tty only", "darwin", env("SSH_TTY", "/dev/ttys001"), link, nil},
		{"windows (not supported)", "windows", env(), link, nil},
		{"http for localhost", "linux", env("DISPLAY", ":0"), "http://localhost:8080/conversations/c-1", []string{"xdg-open", "http://localhost:8080/conversations/c-1"}},
		{"not a web link", "linux", env("DISPLAY", ":0"), "file:///etc/passwd", nil},
		{"another scheme, with a host", "darwin", env(), "ftp://loomux.example.net/conversations/c-1", nil},
		{"a flag, not a link", "linux", env("DISPLAY", ":0"), "--help", nil},
		{"no host", "darwin", env(), "https:///conversations/c-1", nil},
	} {
		if got := Command(c.goos, c.getenv, c.url); !slices.Equal(got, c.want) {
			t.Errorf("%s: Command = %q, want %q", c.name, got, c.want)
		}
	}
}

// open starts the command and doesn't wait for the browser; with no
// command it is ErrNoBrowser and starts nothing.
func TestOpen(t *testing.T) {
	var started [][]string
	start := func(argv []string) error {
		started = append(started, argv)
		return nil
	}
	if err := open("linux", env("DISPLAY", ":0"), link, start); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || !slices.Equal(started[0], []string{"xdg-open", link}) {
		t.Errorf("started %q", started)
	}
	started = nil
	if err := open("linux", env(), link, start); !errors.Is(err, ErrNoBrowser) {
		t.Errorf("no display: %v, want ErrNoBrowser", err)
	}
	if len(started) != 0 {
		t.Errorf("no display, but started %q", started)
	}
	boom := errors.New(`exec: "xdg-open": executable file not found in $PATH`)
	if err := open("linux", env("DISPLAY", ":0"), link, func([]string) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("start failed: %v, want its error", err)
	}
}

// The real start runs a command without waiting for it to end.
func TestStartDoesNotWait(t *testing.T) {
	began := time.Now()
	if err := start([]string{"sh", "-c", "sleep 5"}); err != nil {
		t.Skipf("no sh here: %v", err)
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("start waited %v for the command", d)
	}
	if err := start([]string{"/nonexistent/loomux-browser-test"}); err == nil {
		t.Error("starting a missing command succeeded")
	}
}
