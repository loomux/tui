//go:build unix

package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBrowser puts a browser command first on PATH that records how it
// was run: its argument count, its argument, its process id and (on
// Linux) its session id. It returns the record's path.
func fakeBrowser(t *testing.T) string {
	t.Helper()
	t.Setenv("DISPLAY", ":0")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	argv := Command(runtime.GOOS, os.Getenv, link)
	if argv == nil {
		t.Skip("no browser command on " + runtime.GOOS)
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := `#!/bin/sh
sid=
[ -r /proc/$$/stat ] && IFS=' ' read -r _ _ _ _ _ sid _ < /proc/$$/stat
printf '%s\n%s\n%s\n%s\n' "$#" "$1" "$$" "$sid" > '` + record + `.part'
mv '` + record + `.part' '` + record + `'
`
	if err := os.WriteFile(filepath.Join(dir, argv[0]), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// Open runs the browser command with the link as its one argument, in a
// session of its own: ctrl+c at the terminal, which cancels a login, goes
// to the terminal's foreground processes and mustn't take a browser that
// was just started with it.
func TestOpenRunsTheBrowser(t *testing.T) {
	record := fakeBrowser(t)
	const link = "https://loomux.example.net/link#h=abc&label=my+laptop"
	if err := Open(link); err != nil {
		t.Fatalf("Open = %v, want the browser command started", err)
	}
	var lines []string
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(record); err == nil {
			lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the browser command never ran")
		}
	}
	if len(lines) != 4 || lines[0] != "1" || lines[1] != link {
		t.Fatalf("the browser command got %q, want the link as its one argument", lines)
	}
	if runtime.GOOS == "linux" && lines[3] != lines[2] {
		t.Errorf("the browser command ran in session %s as process %s, want a session of its own", lines[3], lines[2])
	}
}

func TestOpenWithNoneToStart(t *testing.T) {
	fakeBrowser(t)
	t.Setenv("PATH", t.TempDir())
	if err := Open("https://loomux.example.net/link#h=abc"); err == nil {
		t.Error("Open = nil with no browser command on PATH")
	}
}
