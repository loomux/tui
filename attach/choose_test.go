package attach

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/internal/tmuxtest"
)

// onPath is a chooser on a machine with these programs and no tmux
// session of the server's.
func onPath(programs ...string) chooser {
	return chooser{
		lookPath: func(name string) (string, error) {
			if slices.Contains(programs, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		hasSession: func(context.Context, string, string, string) error { return errors.New("no server running") },
	}
}

func remote(cmds ...client.AttachCommand) *client.AttachInfo {
	return &client.AttachInfo{
		TaskID: "task-1", TmuxSession: "loomux-task-1", TmuxSocket: "loomux",
		AttachCommand:  "tmux -L loomux attach -t loomux-task-1",
		AttachCommands: cmds,
		Target:         client.AttachTarget{ID: "t1", Name: "cc-hub", Kind: "remote", Host: "box.example.net", User: "loomux"},
	}
}

var (
	kubectlCmd = client.AttachCommand{Via: "kubectl", Command: "kubectl -n loomux exec -it pod-1 -- tmux -L loomux attach -t loomux-task-1",
		Argv: []string{"kubectl", "-n", "loomux", "exec", "-it", "pod-1", "--", "tmux", "-L", "loomux", "attach", "-t", "loomux-task-1"}}
	sshCmd = client.AttachCommand{Via: "ssh", Command: "ssh loomux@box.example.net -t 'tmux -L loomux attach -t loomux-task-1'",
		Argv: []string{"ssh", "loomux@box.example.net", "-t", "tmux -L loomux attach -t loomux-task-1"}}
)

func vias(cmds []Command) []string {
	out := []string{}
	for _, c := range cmds {
		out = append(out, c.Via)
	}
	return out
}

func skippedVias(p Plan) []string {
	out := []string{}
	for _, s := range p.Skipped {
		out = append(out, s.Via)
	}
	return out
}

func TestPlanTakesTheServersOrder(t *testing.T) {
	p := onPath("kubectl", "ssh").plan(context.Background(), remote(kubectlCmd, sshCmd), Config{})
	if got := vias(p.Commands); !slices.Equal(got, []string{"kubectl", "ssh"}) {
		t.Fatalf("commands = %v, want the server's order", got)
	}
	c := p.Commands[0]
	if !slices.Equal(c.Argv, kubectlCmd.Argv) || c.Path != "/usr/bin/kubectl" {
		t.Errorf("kubectl = %+v, want the server's argv run from PATH", c)
	}
	if len(p.Skipped) != 0 {
		t.Errorf("skipped = %+v", p.Skipped)
	}
}

func TestPlanSkipsAProgramThatIsNotOnPath(t *testing.T) {
	p := onPath("ssh").plan(context.Background(), remote(kubectlCmd, sshCmd), Config{})
	if got := vias(p.Commands); !slices.Equal(got, []string{"ssh"}) {
		t.Fatalf("commands = %v, want ssh alone", got)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Via != "kubectl" || !strings.Contains(p.Skipped[0].Reason, "PATH") || p.Skipped[0].Line != kubectlCmd.Command {
		t.Errorf("skipped = %+v, want kubectl for not being on PATH, with its line", p.Skipped)
	}
}

// [attach] prefer lists the ways the client may use, the most preferred
// first: one it leaves out is disabled.
func TestPlanPrefer(t *testing.T) {
	ch := onPath("kubectl", "ssh")
	p := ch.plan(context.Background(), remote(kubectlCmd, sshCmd), Config{Prefer: []string{"ssh", "kubectl", "docker"}})
	if got := vias(p.Commands); !slices.Equal(got, []string{"ssh", "kubectl"}) {
		t.Errorf("commands = %v, want prefer's order", got)
	}
	p = ch.plan(context.Background(), remote(kubectlCmd, sshCmd), Config{Prefer: []string{"ssh"}})
	if got := vias(p.Commands); !slices.Equal(got, []string{"ssh"}) {
		t.Errorf("commands = %v, want ssh alone", got)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Via != "kubectl" || !strings.Contains(p.Skipped[0].Reason, "prefer") {
		t.Errorf("skipped = %+v, want kubectl for not being in prefer", p.Skipped)
	}
}

func TestPlanKubectlContext(t *testing.T) {
	cfg := Config{KubectlContext: "test-cluster"}
	p := onPath("kubectl", "ssh").plan(context.Background(), remote(kubectlCmd, sshCmd), cfg)
	want := append([]string{"kubectl", "--context", "test-cluster"}, kubectlCmd.Argv[1:]...)
	if !slices.Equal(p.Commands[0].Argv, want) {
		t.Errorf("kubectl argv = %q, want %q", p.Commands[0].Argv, want)
	}
	if !slices.Equal(p.Commands[1].Argv, sshCmd.Argv) {
		t.Errorf("ssh argv = %q: the context is kubectl's alone", p.Commands[1].Argv)
	}
	// The server's own argv isn't changed by the plan.
	if kubectlCmd.Argv[1] != "-n" {
		t.Errorf("the plan changed the attach-info's argv: %q", kubectlCmd.Argv)
	}

	// A command that already names a context keeps it.
	for _, has := range [][]string{
		{"kubectl", "--context", "other", "exec", "-it", "pod-1", "--", "tmux", "attach"},
		{"kubectl", "--context=other", "exec", "-it", "pod-1", "--", "tmux", "attach"},
	} {
		p := onPath("kubectl").plan(context.Background(), remote(client.AttachCommand{Via: "kubectl", Command: strings.Join(has, " "), Argv: has}), cfg)
		if !slices.Equal(p.Commands[0].Argv, has) {
			t.Errorf("argv = %q, want it unchanged", p.Commands[0].Argv)
		}
	}
	// "--context" after "--" is the remote command's, not kubectl's.
	after := []string{"kubectl", "exec", "pod-1", "--", "tool", "--context", "x"}
	p = onPath("kubectl").plan(context.Background(), remote(client.AttachCommand{Via: "kubectl", Command: strings.Join(after, " "), Argv: after}), cfg)
	if want := append([]string{"kubectl", "--context", "test-cluster"}, after[1:]...); !slices.Equal(p.Commands[0].Argv, want) {
		t.Errorf("argv = %q, want %q", p.Commands[0].Argv, want)
	}
}

// A command the server gave without an argv is split into words, never
// handed to a shell.
func TestPlanSplitsACommandWithoutArgv(t *testing.T) {
	docker := client.AttachCommand{Via: "docker", Command: "docker exec -it c1 tmux -L 'loomux 1' attach -t s"}
	piped := client.AttachCommand{Via: "odd", Command: "odd attach | tee log"}
	p := onPath("docker", "odd", "tee").plan(context.Background(), remote(docker, piped), Config{})
	if len(p.Commands) != 1 || !slices.Equal(p.Commands[0].Argv, []string{"docker", "exec", "-it", "c1", "tmux", "-L", "loomux 1", "attach", "-t", "s"}) {
		t.Fatalf("commands = %+v, want docker's line as words", p.Commands)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Via != "odd" || !strings.Contains(p.Skipped[0].Reason, "shell") {
		t.Errorf("skipped = %+v, want the piped line for needing a shell", p.Skipped)
	}
}

// The program is a name looked up on PATH: an argv that names a file, or
// nothing, isn't run.
func TestPlanRefusesAProgramThatIsNotAName(t *testing.T) {
	ch := chooser{
		lookPath:   func(name string) (string, error) { return name, nil }, // everything "exists"
		hasSession: func(context.Context, string, string, string) error { return nil },
	}
	for _, argv := range [][]string{
		{"/usr/bin/ssh", "box"},
		{"./ssh", "box"},
		{"bin/ssh", "box"},
		{"", "box"},
		{".", "box"},
		{"..", "box"},
		{"ssh", "bo\x00x"},
		{"-ssh"},
	} {
		p := ch.plan(context.Background(), remote(client.AttachCommand{Via: "ssh", Command: "ssh box", Argv: argv}), Config{})
		if len(p.Commands) != 0 || len(p.Skipped) != 1 {
			t.Errorf("%q: plan = %+v, want it skipped", argv, p)
		}
	}
}

func local() *client.AttachInfo {
	return &client.AttachInfo{
		TaskID: "task-1", TmuxSession: "loomux-task-1", TmuxSocket: "loomux",
		AttachCommand:  "tmux -L loomux attach -t loomux-task-1",
		AttachCommands: []client.AttachCommand{},
		Target:         client.AttachTarget{ID: "t0", Name: "local", Kind: "local"},
	}
}

// A local target has no attach_commands: its one attach_command runs
// only where the server's tmux session is, which the client checks.
func TestPlanLocalTarget(t *testing.T) {
	var asked []string
	ch := onPath("tmux", "ssh")
	ch.hasSession = func(_ context.Context, tmux, socket, session string) error {
		asked = append(asked, tmux, socket, session)
		return nil
	}
	// prefer is about attach_commands: it doesn't disable the only way to
	// a local target.
	p := ch.plan(context.Background(), local(), Config{Prefer: []string{"ssh"}})
	if len(p.Commands) != 1 || p.Commands[0].Via != ViaLocal ||
		!slices.Equal(p.Commands[0].Argv, []string{"tmux", "-L", "loomux", "attach", "-t", "loomux-task-1"}) {
		t.Fatalf("plan = %+v, want the local attach_command", p)
	}
	if !slices.Equal(asked, []string{"/usr/bin/tmux", "loomux", "loomux-task-1"}) {
		t.Errorf("has-session asked %q, want the server's socket and session", asked)
	}

	// On another machine the session isn't there.
	p = onPath("tmux").plan(context.Background(), local(), Config{})
	if len(p.Commands) != 0 || len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0].Reason, "server's own machine") ||
		p.Skipped[0].Line != "tmux -L loomux attach -t loomux-task-1" {
		t.Errorf("plan = %+v, want the command skipped: the session is on the server's own machine", p)
	}

	// Without tmux there is nothing to check with.
	p = onPath().plan(context.Background(), local(), Config{})
	if len(p.Commands) != 0 || len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0].Reason, "PATH") {
		t.Errorf("plan = %+v, want tmux missing from PATH", p)
	}

	// The local command is tmux's attach and nothing else.
	odd := local()
	odd.AttachCommand = "screen -x loomux"
	p = onPath("tmux", "screen").plan(context.Background(), odd, Config{})
	if len(p.Commands) != 0 || len(p.Skipped) != 1 {
		t.Errorf("plan = %+v, want a local command that isn't tmux skipped", p)
	}
}

// A remote target with no attach_commands has no way in: the local
// attach_command is the server's own machine's, never run for another
// kind of target.
func TestPlanRemoteTargetWithoutCommands(t *testing.T) {
	ch := onPath("tmux")
	ch.hasSession = func(context.Context, string, string, string) error { return nil }
	p := ch.plan(context.Background(), remote(), Config{})
	if len(p.Commands) != 0 || len(p.Skipped) != 0 {
		t.Errorf("plan = %+v, want nothing", p)
	}
}

// The real check: tmux has-session on the socket, for exactly the
// session.
func TestHasSessionAgainstTmux(t *testing.T) {
	srv := tmuxtest.New(t)
	srv.NewSession("loomux-task-1", "sleep 600")
	t.Setenv("TMUX_TMPDIR", srv.Dir)
	info := local()
	info.TmuxSocket = srv.Socket
	info.AttachCommand = "tmux -L " + srv.Socket + " attach -t loomux-task-1"
	p := newChooser().plan(context.Background(), info, Config{})
	if len(p.Commands) != 1 || p.Commands[0].Via != ViaLocal {
		t.Fatalf("plan = %+v, want the local command: the session is here", p)
	}
	// A session whose name the asked one only starts isn't it.
	info.TmuxSession = "loomux-task"
	info.AttachCommand = "tmux -L " + srv.Socket + " attach -t loomux-task"
	p = newChooser().plan(context.Background(), info, Config{})
	if len(p.Commands) != 0 {
		t.Errorf("plan = %+v, want nothing: only a longer-named session is here", p)
	}
}

func TestCommandString(t *testing.T) {
	c := Command{Via: "ssh", Argv: []string{"ssh", "-p", "2222", "loomux@box.example.net", "-t", "tmux -L loomux attach -t it's", "", "a\x1b[2Jb"}}
	// As a shell reads it back: the same words, with no control character
	// for the terminal.
	want := `ssh -p 2222 loomux@box.example.net -t 'tmux -L loomux attach -t it'\''s' '' 'a[2Jb'`
	if got := c.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	words, err := splitWords(c.String())
	if err != nil || !slices.Equal(words, []string{"ssh", "-p", "2222", "loomux@box.example.net", "-t", "tmux -L loomux attach -t it's", "", "a[2Jb"}) {
		t.Errorf("split back = %q, %v", words, err)
	}
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"cc-hub":                  "cc-hub",
		"a\x1b[31mred\x07":        "a[31mred",
		"two\nlines\tand a tab":   "two lines and a tab",
		"rtl\u202eoverride\u2066": "rtloverride",
		"c1\u009bcsi":             "c1csi",
		"bad\xffutf8":             "badutf8",
		"naïve ☕":                 "naïve ☕",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
