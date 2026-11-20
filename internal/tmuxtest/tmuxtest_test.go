package tmuxtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerIsTheTestsOwn(t *testing.T) {
	var dir, socket string
	t.Run("a session", func(t *testing.T) {
		s := New(t)
		dir, socket = s.Dir, s.Socket
		// A Unix socket address holds about 100 bytes: the path tmux
		// makes from TMUX_TMPDIR has to fit whatever TMPDIR is.
		if p := s.SocketPath(); len(p) > 80 {
			t.Errorf("socket path %q is %d bytes, want a short one", p, len(p))
		}
		s.NewSession("agent", "sleep 600")
		if !s.HasSession("agent") {
			t.Error("the session isn't there")
		}
		if s.HasSession("age") {
			t.Error("HasSession matched a prefix of the name")
		}
		if n := s.Clients("agent"); n != 0 {
			t.Errorf("%d clients, want none", n)
		}
	})
	// The subtest's cleanup killed the server and removed its directory.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s is still there after the test (%v)", dir, err)
	}
	if socket == "" {
		t.Error("no socket name")
	}
}

// A process started with Env reaches the server by its socket name, and
// isn't taken for a client of the tmux the test itself may run in.
func TestEnvReachesTheServer(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-0/default,1,0")
	t.Setenv("TMUX_PANE", "%0")
	s := New(t)
	s.NewSession("one", "sleep 600")
	var tmuxDir string
	for _, kv := range s.Env() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Errorf("Env has %s: a process in it would think it is inside tmux", kv)
		}
		if v, ok := strings.CutPrefix(kv, "TMUX_TMPDIR="); ok {
			tmuxDir = v
		}
	}
	if tmuxDir != s.Dir {
		t.Errorf("TMUX_TMPDIR = %q, want %q", tmuxDir, s.Dir)
	}
	if filepath.Dir(filepath.Dir(s.SocketPath())) != s.Dir {
		t.Errorf("socket path %q isn't under %q", s.SocketPath(), s.Dir)
	}
	out := s.Tmux("list-sessions", "-F", "#{session_name}")
	if out != "one" {
		t.Errorf("list-sessions = %q, want one", out)
	}
}

// A second tmux client attached from a session of another server shows as
// a client, and detaching it (what the server does when a hold ends) ends
// it: the pieces the attach tests are built from.
func TestAttachFromAnotherServer(t *testing.T) {
	agent := New(t)
	agent.NewSession("agent", "sleep 600")
	term := New(t)
	// The terminal's pane attaches to the agent's session, as a person's
	// terminal would; when the attach ends it writes a marker.
	marker := filepath.Join(t.TempDir(), "done")
	term.NewSession("term", "TMUX_TMPDIR="+agent.Dir+" tmux -L "+agent.Socket+" attach -t agent; echo detached > "+marker+"; sleep 600")
	agent.WaitClients("agent", 1, 10*time.Second)
	agent.Tmux("detach-client", "-s", "=agent")
	agent.WaitClients("agent", 0, 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the attach didn't end after the detach; the pane shows:\n%s", term.Capture("term"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
