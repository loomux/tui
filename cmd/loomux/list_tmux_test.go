//go:build unix

package main

// The screens in a real terminal: the list in a tmux pane, a on its row.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/internal/tmuxtest"
)

// listInTerminal starts the list in a terminal, with [attach] mode as
// given, and waits for the seeded conversation's row.
func listInTerminal(t *testing.T, e *attachEnv, mode string) *inTerminal {
	t.Helper()
	e.srv.SeedMessage(e.task.ConversationID, registry.MessageRoleUser, "fix the build", e.task)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "loomux", "config.toml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[attach]\nmode = \"" + mode + "\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	it := startInTerminal(t, e, "")
	it.screen("fix the build")
	return it
}

// a on a row, inline: the screens leave the terminal to the attach
// command, and are back, with what happened, when it is detached.
func TestTerminalListAttachInline(t *testing.T) {
	e := newAttachEnv(t)
	it := listInTerminal(t, e, "inline")

	it.terminal.Tmux("send-keys", "-t", "=terminal:", "a")
	it.agent.WaitClients(agentSession, 1, 20*time.Second)
	if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ClientLabel != "laptop" {
		t.Fatalf("the hold while attached = %+v, want the laptop's", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusHumanTakeover {
		t.Errorf("status while attached = %s, want human-takeover", st)
	}

	it.terminal.Tmux("send-keys", "-t", "=terminal:", "C-b", "d")
	it.agent.WaitClients(agentSession, 0, 10*time.Second)
	screen := it.screen("the hold is released")
	if !strings.Contains(screen, "fix the build") || !strings.Contains(screen, "loomux · test") {
		t.Errorf("the list isn't back:\n%s", screen)
	}
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("still held after the detach: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status after the detach = %s, want awaiting-input", st)
	}

	// The screens still take keys: the terminal is theirs again.
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "?")
	it.screen("attach to the agent")
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "q")
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "q")
	if code := it.exit(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// a on a row inside tmux: the attach gets a window of its own, which
// takes, holds and releases by itself, and closes when it is detached.
// The list stays up in its window.
func TestTerminalListAttachWindow(t *testing.T) {
	e := newAttachEnv(t)
	// tmux, for the screens to open the window with: PATH is otherwise
	// the fake ssh alone.
	if err := os.Symlink(tmuxtest.Binary(), filepath.Join(e.bin, "tmux")); err != nil {
		t.Fatal(err)
	}
	it := listInTerminal(t, e, "auto")
	windows := func() []string {
		return strings.Split(it.terminal.Tmux("list-windows", "-t", "=terminal", "-F", "#{window_name}"), "\n")
	}
	if n := len(windows()); n != 1 {
		t.Fatalf("%d windows before the attach, want 1", n)
	}

	it.terminal.Tmux("send-keys", "-t", "=terminal:", "a")
	it.agent.WaitClients(agentSession, 1, 20*time.Second)
	if w := windows(); len(w) != 2 || w[1] != "web" {
		t.Errorf("windows = %q, want a second one named for the workspace", w)
	}
	if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ClientLabel != "laptop" {
		t.Fatalf("the hold while attached = %+v, want the laptop's", h)
	}
	// The list is still there, in the first window.
	first := it.terminal.Tmux("list-windows", "-t", "=terminal", "-F", "#{window_id}")
	first, _, _ = strings.Cut(first, "\n")
	if list := it.terminal.Tmux("capture-pane", "-p", "-t", first); !strings.Contains(list, "fix the build") || !strings.Contains(list, "tmux window") {
		t.Errorf("the list's window shows:\n%s", list)
	}

	// Detach in the attach's window, the current one.
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "C-b", "d")
	it.agent.WaitClients(agentSession, 0, 10*time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for len(windows()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the attach's window didn't close: %q\n%s", windows(), it.terminal.Capture("terminal"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("still held after the detach: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status after the detach = %s, want awaiting-input", st)
	}
}

// q while the takeover is being answered: the screens end, and the hold
// the server granted meanwhile is given back before the client exits.
func TestTerminalListQuitWhileTakingOver(t *testing.T) {
	e := newAttachEnv(t)
	answered := make(chan struct{})
	e.after = func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/takeover") {
			<-answered // the hold is taken; its answer waits
		}
	}
	it := listInTerminal(t, e, "inline")
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "a")
	deadline := time.Now().Add(15 * time.Second)
	for e.srv.ActiveHold(e.task.ID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("no takeover reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "q")
	time.Sleep(200 * time.Millisecond) // the screens have ended; the client waits for the answer
	close(answered)
	if code := it.exit(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("the client exited with its hold: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want it handed back", st)
	}
	if it.agent.Clients(agentSession) != 0 {
		t.Error("the attach command ran after the screens quit")
	}
}
