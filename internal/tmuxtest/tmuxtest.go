// Package tmuxtest gives a test a tmux server of its own (the design in
// loomux/server, docs/design/tui-client-design.md, "Repository": "Attach
// tests need a tmux helper of the client's own"). The attach tests use
// two: one stands for the machine an agent's session lives on, the other
// gives the client under test a real terminal (a pane).
//
// A server has its own short TMUX_TMPDIR, so it never meets the tmux the
// test runs in, another test's, or a Loomux instance's, and its socket's
// path fits a Unix socket address whatever TMPDIR is. It loads no tmux
// configuration. The test's cleanup kills it and removes the directory.
//
// The package is for tests: only _test.go files import it.
package tmuxtest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// socket is every server's socket name: each has a directory of its own.
const socket = "loomuxtest"

// Server is a tmux server of the test's own.
type Server struct {
	// Dir is the server's TMUX_TMPDIR.
	Dir string
	// Socket is its socket name: tmux -L Socket, with TMUX_TMPDIR=Dir.
	Socket string

	tb testing.TB
}

// New returns a tmux server for tb, started by its first session. Without
// tmux installed the test is skipped; in CI, where it must be, it fails.
func New(tb testing.TB) *Server {
	tb.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") != "" {
			tb.Fatalf("tmuxtest: tmux isn't installed: %v", err)
		}
		tb.Skipf("tmuxtest: tmux isn't installed: %v", err)
	}
	// /tmp rather than TMPDIR: a long TMPDIR would push the socket's path
	// past what a Unix socket address holds.
	dir, err := os.MkdirTemp("/tmp", "lxt")
	if err != nil {
		if dir, err = os.MkdirTemp("", "lxt"); err != nil {
			tb.Fatalf("tmuxtest: %v", err)
		}
	}
	s := &Server{Dir: dir, Socket: socket, tb: tb}
	tb.Cleanup(func() {
		_ = s.Command("kill-server").Run()
		if err := os.RemoveAll(dir); err != nil {
			tb.Errorf("tmuxtest: %v", err)
		}
	})
	return s
}

// SocketPath is where tmux puts the server's socket file.
func (s *Server) SocketPath() string {
	return filepath.Join(s.Dir, "tmux-"+strconv.Itoa(os.Getuid()), s.Socket)
}

// Env is the environment of a process that reaches this server with
// `tmux -L Socket`: the test's own, with TMUX_TMPDIR set to Dir and
// without TMUX and TMUX_PANE, so that it isn't taken for a client of the
// tmux the test may itself run in.
func (s *Server) Env() []string {
	env := []string{"TMUX_TMPDIR=" + s.Dir}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "TMUX", "TMUX_PANE", "TMUX_TMPDIR":
		default:
			env = append(env, kv)
		}
	}
	return env
}

// Command is a tmux command on this server, not yet run.
func (s *Server) Command(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-L", s.Socket, "-f", "/dev/null"}, args...)...)
	cmd.Env = s.Env()
	return cmd
}

// Tmux runs a tmux command on this server and returns its output,
// trimmed. A command that fails fails the test.
func (s *Server) Tmux(args ...string) string {
	s.tb.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.tb.Fatalf("tmuxtest: tmux %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (s *Server) run(args ...string) (string, error) {
	cmd := s.Command(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// NewSession starts a detached 80×24 session called name running
// command, a shell line. The session ends when the command does.
func (s *Server) NewSession(name, command string) {
	s.tb.Helper()
	s.Tmux("new-session", "-d", "-s", name, "-x", "80", "-y", "24", command)
}

// HasSession reports whether the server has a session called exactly
// name.
func (s *Server) HasSession(name string) bool {
	_, err := s.run("has-session", "-t", "="+name)
	return err == nil
}

// Clients is how many tmux clients are attached to the session. A
// session or server that is gone has none.
func (s *Server) Clients(session string) int {
	out, err := s.run("list-clients", "-t", "="+session, "-F", "#{client_pid}")
	if err != nil || out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// WaitClients waits until n clients are attached to the session, and
// fails the test if that takes longer than timeout.
func (s *Server) WaitClients(session string, n int, timeout time.Duration) {
	s.tb.Helper()
	deadline := time.Now().Add(timeout)
	for s.Clients(session) != n {
		if time.Now().After(deadline) {
			s.tb.Fatalf("tmuxtest: session %s has %d clients after %s, want %d", session, s.Clients(session), timeout, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Capture is what the session's pane shows, or "" once it is gone.
func (s *Server) Capture(session string) string {
	out, _ := s.run("capture-pane", "-p", "-t", "="+session+":")
	return out
}
