//go:build unix

package main

// The design's attach tests ("Testing" → Attach): the client in a real
// terminal, a pane of one tmux server, attaching to a session of another,
// which stands for the machine the agent runs on. The attach command is
// the fake ssh: a script that attaches to that local tmux session.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/internal/tmuxtest"
)

// inTerminal is a client started in a pane of the terminal's tmux,
// attaching through the fake ssh to the session "agent" of the agent's
// tmux.
type inTerminal struct {
	t        *testing.T
	e        *attachEnv
	agent    *tmuxtest.Server
	terminal *tmuxtest.Server
	dir      string
}

const agentSession = "agent"

// attachInTerminal starts `loomux <args>` in a terminal and waits until
// its attach command has attached.
func attachInTerminal(t *testing.T, e *attachEnv, args string) *inTerminal {
	t.Helper()
	it := startInTerminal(t, e, args)
	it.agent.WaitClients(agentSession, 1, 20*time.Second)
	return it
}

// startInTerminal starts `loomux <args>` in a pane of the terminal's
// tmux, with the fake ssh that attaches to the agent's session.
func startInTerminal(t *testing.T, e *attachEnv, args string) *inTerminal {
	t.Helper()
	it := &inTerminal{t: t, e: e, agent: tmuxtest.New(t), terminal: tmuxtest.New(t), dir: t.TempDir()}
	// By its full name: PATH holds the test's fake programs alone.
	it.agent.NewSession(agentSession, "/bin/sleep 600")

	tmux := tmuxtest.Binary()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The fake ssh: whatever host it is given, it attaches to the agent's
	// session, as ssh would on the host. TMUX is the terminal's tmux, and
	// the remote side of a real ssh wouldn't see it.
	e.ssh("printf '%s\\n' \"$@\" > " + e.file("args") + "\n" +
		"unset TMUX TMUX_PANE\n" +
		"TMUX_TMPDIR=" + it.agent.Dir + " TERM=xterm exec " + tmux + " -L " + it.agent.Socket + " -f /dev/null attach -t =" + agentSession)

	// The pane runs the client with the test's profile, then leaves its
	// exit status and stays, so the screen can still be read.
	script := "#!/bin/sh\n" +
		"export XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME") + " XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME") + "\n" +
		"export PATH=" + e.bin + " LOOMUX_TEST_PIDFILE=" + it.file("pid") + "\n" +
		"LOOMUX_TEST_RUN='" + args + "' " + self + "\n" +
		"echo $? > " + it.file("exit") + "\n" +
		"exec /bin/sleep 600\n"
	if err := os.WriteFile(it.file("pane.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	it.terminal.NewSession("terminal", it.file("pane.sh"))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the terminal shows:\n%s", it.terminal.Capture("terminal"))
		}
	})
	return it
}

func (it *inTerminal) file(name string) string { return filepath.Join(it.dir, name) }

// exit waits for the client to exit and returns its status.
func (it *inTerminal) exit() int {
	it.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if b, err := os.ReadFile(it.file("exit")); err == nil && len(b) > 0 {
			code, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				it.t.Fatalf("exit status %q: %v", b, err)
			}
			return code
		}
		if time.Now().After(deadline) {
			it.t.Fatal("the client didn't exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// screen waits for the terminal to show text and returns what it shows.
func (it *inTerminal) screen(text string) string {
	it.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		screen := it.terminal.Capture("terminal")
		if strings.Contains(screen, text) {
			return screen
		}
		if time.Now().After(deadline) {
			it.t.Fatalf("the terminal doesn't show %q:\n%s", text, screen)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Takeover → the command on the terminal → detach with tmux's own
// prefix d → release.
func TestTerminalAttachDetachRelease(t *testing.T) {
	e := newAttachEnv(t)
	it := attachInTerminal(t, e, "attach --task "+e.task.ID)

	if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ClientLabel != "laptop" {
		t.Fatalf("the hold while attached = %+v, want the laptop's", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusHumanTakeover {
		t.Errorf("status while attached = %s, want human-takeover", st)
	}

	// The person's keys reach the agent's tmux: its prefix, then d.
	it.terminal.Tmux("send-keys", "-t", "=terminal:", "C-b", "d")
	if code := it.exit(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	it.agent.WaitClients(agentSession, 0, 10*time.Second)
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("still held after the detach: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status after the detach = %s, want awaiting-input", st)
	}
	screen := it.screen("the hold is released")
	if !strings.Contains(screen, "attaching to box via ssh") {
		t.Errorf("the terminal doesn't say where it attached:\n%s", screen)
	}
	if !it.agent.HasSession(agentSession) {
		t.Error("the agent's session is gone: a detach must leave it running")
	}
}

// kill -9 of the client → its command is hung up at once (Linux) → the
// hold expires → the server detaches the session's clients → the task is
// awaiting input again.
func TestTerminalClientKilled(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	e := newAttachEnv(t, apitest.WithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}))
	it := attachInTerminal(t, e, "attach --task "+e.task.ID)

	pid, err := os.ReadFile(it.file("pid"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(string(pid))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(n, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if code := it.exit(); code != 128+int(syscall.SIGKILL) {
		t.Errorf("the client's exit status = %d, want it killed", code)
	}
	if runtime.GOOS == "linux" {
		// Nobody is left typing into the pane while the lease runs out.
		it.agent.WaitClients(agentSession, 0, 10*time.Second)
	}

	// Nothing released the hold: it is the server's to end.
	if h := e.srv.ActiveHold(e.task.ID); h == nil {
		t.Fatal("the hold is gone, but the client was killed before it could release")
	}
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	if ended := e.srv.SweepHolds(); ended != 1 {
		t.Errorf("the sweep ended %d holds, want 1", ended)
	}
	if s := e.srv.Executor.Session("loomux-" + e.task.ID); s == nil || s.Detaches != 1 {
		t.Errorf("the server's session = %+v, want its clients detached once", s)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want awaiting-input", st)
	}
}

// A forced takeover from a second session detaches the first, whose
// release then doesn't hand the task back from under the new holder.
func TestTerminalTakenOverByAnotherSession(t *testing.T) {
	e := newAttachEnv(t)
	it := attachInTerminal(t, e, "attach --task "+e.task.ID)

	desk := e.other("desk")
	theirs, err := desk.Takeover(attachCtx(t), e.task.ID, client.TakeoverRequest{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	// The server detaches the holder's clients before it grants the new
	// hold. The test server's machine only counts that, so the detach
	// itself is done here, as targets.DetachClients does it.
	if s := e.srv.Executor.Session("loomux-" + e.task.ID); s == nil || s.Detaches != 1 {
		t.Fatalf("the server's session = %+v, want its clients detached once", s)
	}
	it.agent.Tmux("detach-client", "-s", "="+agentSession)

	if code := it.exit(); code != 1 {
		t.Errorf("exit %d, want 1: the hold didn't end as this client's own release", code)
	}
	it.screen("another session took the task over")
	if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ID != theirs.Hold.ID {
		t.Errorf("the hold = %+v, want the second session's still", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusHumanTakeover {
		t.Errorf("status = %s, want human-takeover: the first client's release handed the task back", st)
	}
}
