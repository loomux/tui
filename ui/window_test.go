//go:build unix

package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/tui/internal/tmuxtest"
)

// inTmux makes the test's process a client of srv's session, as the
// screens are of the tmux they run in: TMUX names the server's socket.
func inTmux(t *testing.T, srv *tmuxtest.Server, session string) {
	t.Helper()
	srv.NewSession(session, "/bin/sleep 600")
	id := strings.TrimPrefix(srv.Tmux("display-message", "-p", "-t", "="+session+":", "#{session_id}"), "$")
	pid := srv.Tmux("display-message", "-p", "-t", "="+session+":", "#{pid}")
	t.Setenv("TMUX", srv.SocketPath()+","+pid+","+id)
	t.Setenv("PATH", filepath.Dir(tmuxtest.Binary())+":/usr/bin:/bin")
}

// The window runs the command as it is, with the environment given by
// name, and is named for the workspace.
func TestTmuxNewWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/the/tmux/servers/own") // what the tmux server starts with
	srv := tmuxtest.New(t)
	inTmux(t, srv, "screens")
	dir := t.TempDir()
	script := filepath.Join(dir, "attach.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$LOOMUX_SERVER\" \"state=$XDG_STATE_HOME\" \"$@\" > " + filepath.Join(dir, "ran") + "\nexec /bin/sleep 600\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := tmuxNewWindow(context.Background(), "web", []string{"LOOMUX_SERVER=https://loomux.example.net", "XDG_STATE_HOME="},
		[]string{script, "attach", "--task", "an id with spaces; and $(a shell's) `marks`"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "ran")); err == nil && len(b) > 0 {
			// A setting given empty is empty in the window, whatever the
			// tmux server's own environment has for it.
			if want := "https://loomux.example.net\nstate=\nattach\n--task\nan id with spaces; and $(a shell's) `marks`\n"; string(b) != want {
				t.Errorf("the window's command got %q, want %q", b, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the window's command didn't run")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if names := srv.Tmux("list-windows", "-t", "=screens", "-F", "#{window_name}"); !strings.HasSuffix(names, "\nweb") {
		t.Errorf("windows = %q, want the new one named web", names)
	}

	// The window's id closes it again, and only it.
	if ids := srv.Tmux("list-windows", "-t", "=screens", "-F", "#{window_id}"); !strings.HasSuffix(ids, "\n"+id) {
		t.Errorf("tmuxNewWindow returned id %q; the windows are %q", id, ids)
	}
	if err := tmuxKillWindow(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if names := srv.Tmux("list-windows", "-t", "=screens", "-F", "#{window_name}"); strings.Contains(names, "web") || names == "" {
		t.Errorf("after the kill the windows are %q, want the first one alone", names)
	}
}

// Nothing the server names is a tmux format: a workspace, a setting or
// an id written as #(command) runs no command.
func TestTmuxNewWindowRunsNoFormat(t *testing.T) {
	srv := tmuxtest.New(t)
	inTmux(t, srv, "screens")
	dir := t.TempDir()
	mark := func(name string) string { return "#(/usr/bin/touch " + filepath.Join(dir, name) + ")" }
	_, err := tmuxNewWindow(context.Background(), "hub "+mark("from-name"),
		[]string{"LOOMUX_SERVER=" + mark("from-env")},
		[]string{"/bin/sh", "-c", "exec /bin/sleep 600", "sh", mark("from-argv")}) // sh ignores the last one
	if err != nil {
		t.Fatal(err)
	}
	// With a client attached tmux draws its status line, window names
	// and all: the other moment a format in a name could be run.
	viewer := tmuxtest.New(t)
	viewer.NewSession("viewer", "TMUX= TMUX_TMPDIR="+srv.Dir+" TERM=xterm "+tmuxtest.Binary()+" -L "+srv.Socket+" -f /dev/null attach -t =screens")
	srv.WaitClients("screens", 1, 10*time.Second)
	time.Sleep(500 * time.Millisecond)
	for _, name := range []string{"from-name", "from-env", "from-argv"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("tmux ran the command written in the window's %s", strings.TrimPrefix(name, "from-"))
		}
	}
	// The window is named as written (the first 40 characters of it).
	names := srv.Tmux("list-windows", "-t", "=screens", "-F", "#{window_name}")
	if want := string([]rune("hub " + mark("from-name"))[:40]); !strings.HasSuffix(names, "\n"+want) {
		t.Errorf("windows = %q, want the new one named %q, as written", names, want)
	}
	// And what the status line shows is the name, not what it expands to.
	if status := viewer.Capture("viewer"); !strings.Contains(status, "#(/usr/bin/touch") {
		t.Errorf("the status line doesn't show the name as written:\n%s", status)
	}
}

func TestWindowName(t *testing.T) {
	for in, want := range map[string]string{
		"web":            "web",
		"":               "loomux",
		"c#-utils":       "c##-utils",
		"#(rm -rf ~) #S": "##(rm -rf ~) ##S",
		"####":           "########",
		"a very long workspace name that goes on and on and on": "a very long workspace name that goes on ",
	} {
		if got := windowName(in); got != want {
			t.Errorf("windowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A tmux that can't open the window says why.
func TestTmuxNewWindowFails(t *testing.T) {
	srv := tmuxtest.New(t)
	inTmux(t, srv, "screens")
	t.Setenv("TMUX", filepath.Join(t.TempDir(), "no-such-socket")+",1,0")
	_, err := tmuxNewWindow(context.Background(), "web", nil, []string{"/bin/sleep", "600"})
	if err == nil || !strings.Contains(err.Error(), "tmux new-window") {
		t.Errorf("err = %v, want tmux's own error", err)
	}
}
