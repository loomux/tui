package attach

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Loomux/tui/client"
)

// Config is the config file's [attach] table as the choice of a command
// uses it.
type Config struct {
	// Prefer lists the ways in (an attach command's via: kubectl, ssh,
	// docker, …) the client may use, the most preferred first. A way it
	// leaves out is disabled. Empty means every way the server gives, in
	// the server's order. It is about the server's attach_commands: a
	// local target's one command isn't subject to it.
	Prefer []string
	// KubectlContext adds --context to a kubectl command that names none.
	KubectlContext string
}

// ViaLocal is the Via of a local target's command: tmux on the server's
// own machine, attach-info's attach_command.
const ViaLocal = "local"

// Command is a way to attach that this machine can run: the server's
// argv, executed directly with no shell.
type Command struct {
	// Via names the way in: kubectl, ssh, docker, …, or ViaLocal.
	Via string
	// Argv is the command. Argv[0] is its program's name.
	Argv []string
	// Path is the program's file, found on PATH.
	Path string
}

// String is the command as a shell line, to show: quoted so that pasting
// it runs the same argv, and without control characters.
func (c Command) String() string { return shellLine(c.Argv) }

// Skipped is a way to attach that this machine can't use, and why.
type Skipped struct {
	Via string
	// Line is the command as the server shows it, cleaned for a terminal.
	Line   string
	Reason string
}

// Plan is the choice: the commands to try, in order, and the ones left
// out.
type Plan struct {
	Commands []Command
	Skipped  []Skipped
}

// chooser makes a Plan on this machine. Its fields are what a test
// replaces.
type chooser struct {
	// lookPath finds a program on PATH (exec.LookPath).
	lookPath func(name string) (string, error)
	// hasSession checks that tmux's server on socket has session.
	hasSession func(ctx context.Context, tmux, socket, session string) error
}

func newChooser() chooser {
	return chooser{lookPath: exec.LookPath, hasSession: tmuxHasSession}
}

// plan chooses from an attach-info (the design's "Choosing the command").
//
// From attach_commands, in the server's order (the plugin's first, then
// ssh), it takes every command whose way in the config doesn't disable
// and whose program is on PATH. A command has an argv, or else its line
// is split into words. Prefer, when set, reorders them.
//
// A local target has no attach_commands: its attach_command is tmux on
// the server's own machine, taken only when that tmux server is reachable
// from here and has the session.
func (ch chooser) plan(ctx context.Context, info *client.AttachInfo, cfg Config) Plan {
	var p Plan
	skip := func(c client.AttachCommand, argv []string, reason string) {
		line := clean(c.Command)
		if line == "" {
			line = shellLine(argv)
		}
		p.Skipped = append(p.Skipped, Skipped{Via: clean(c.Via), Line: line, Reason: reason})
	}
	for _, c := range info.AttachCommands {
		if len(cfg.Prefer) > 0 && !slices.Contains(cfg.Prefer, c.Via) {
			skip(c, c.Argv, fmt.Sprintf("%s isn't in [attach] prefer in the config", clean(c.Via)))
			continue
		}
		cmd, reason := ch.command(c.Via, c.Argv, c.Command)
		if reason != "" {
			skip(c, c.Argv, reason)
			continue
		}
		if cmd.Argv[0] == "kubectl" {
			cmd.Argv = withKubectlContext(cmd.Argv, cfg.KubectlContext)
		}
		p.Commands = append(p.Commands, cmd)
	}
	if len(cfg.Prefer) > 0 {
		// Commands holds only ways Prefer lists; their vias are cleaned,
		// which changes none that could be listed in a config.
		slices.SortStableFunc(p.Commands, func(a, b Command) int {
			return slices.Index(cfg.Prefer, a.Via) - slices.Index(cfg.Prefer, b.Via)
		})
	}

	if len(info.AttachCommands) == 0 && info.Target.Kind == "local" && info.AttachCommand != "" {
		c := client.AttachCommand{Via: ViaLocal, Command: info.AttachCommand}
		cmd, reason := ch.command(ViaLocal, nil, info.AttachCommand)
		switch {
		case reason != "":
		case cmd.Argv[0] != "tmux":
			reason = "a local target's command is tmux's, and this one isn't"
		case info.TmuxSession == "":
			reason = "the server named no tmux session"
		case ch.hasSession(ctx, cmd.Path, info.TmuxSocket, info.TmuxSession) != nil:
			reason = fmt.Sprintf("the task runs on the server's own machine (target %s), and its tmux session isn't on this one", clean(info.Target.Name))
		}
		if reason != "" {
			skip(c, nil, reason)
		} else {
			p.Commands = append(p.Commands, cmd)
		}
	}
	return p
}

// command makes the Command of one way in, or says why it can't be run.
func (ch chooser) command(via string, argv []string, line string) (Command, string) {
	if len(argv) == 0 {
		words, err := splitWords(line)
		if err != nil {
			return Command{}, "it can't be run without a shell: " + err.Error()
		}
		argv = words
	}
	if err := checkArgv(argv); err != nil {
		return Command{}, err.Error()
	}
	path, err := ch.lookPath(argv[0])
	if err != nil {
		return Command{}, fmt.Sprintf("%s isn't installed (not on PATH)", clean(argv[0]))
	}
	return Command{Via: clean(via), Argv: slices.Clone(argv), Path: path}, ""
}

// checkArgv refuses an argv that doesn't start with a program's name: the
// program is looked up on PATH, so a path (which could name any file) or
// an option isn't one. No argument may hold a NUL, which can't be passed
// to a program.
func checkArgv(argv []string) error {
	prog := argv[0]
	switch {
	case prog == "":
		return errors.New("it names no program")
	case strings.ContainsAny(prog, `/\`) || prog == "." || prog == "..":
		return errors.New("its program is a path, not a program's name to find on PATH")
	case strings.HasPrefix(prog, "-"):
		return errors.New("it starts with an option, not a program's name")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return errors.New("an argument has a NUL character")
		}
	}
	return nil
}

// withKubectlContext adds --context after the program of a kubectl argv,
// unless it already names one among kubectl's own options (those before
// "--").
func withKubectlContext(argv []string, kubeContext string) []string {
	if kubeContext == "" {
		return argv
	}
	for _, arg := range argv[1:] {
		if arg == "--" {
			break
		}
		if arg == "--context" || strings.HasPrefix(arg, "--context=") {
			return argv
		}
	}
	return slices.Concat([]string{argv[0], "--context", kubeContext}, argv[1:])
}

// tmuxHasSession asks the tmux server on socket for exactly session. It
// starts no server: without one, tmux says so and fails.
func tmuxHasSession(ctx context.Context, tmux, socket, session string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var args []string
	if socket != "" {
		args = append(args, "-L", socket)
	}
	// "=" names the session exactly; without it tmux also takes one whose
	// name the session's only starts.
	args = append(args, "has-session", "-t", "="+session)
	return exec.CommandContext(ctx, tmux, args...).Run()
}

// shellLine is argv as one line a POSIX shell reads back as the same
// words, cleaned for a terminal.
func shellLine(argv []string) string {
	words := make([]string, len(argv))
	for i, arg := range argv {
		words[i] = shellQuote(clean(arg))
	}
	return strings.Join(words, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// clean makes text from the server safe to print on a terminal: control
// characters (so no escape sequence), bidirectional-text controls and
// bytes that aren't UTF-8 are dropped, and a tab or line break becomes a
// space.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n':
			b.WriteByte(' ')
		case r < ' ' || r >= 0x7f && r < 0xa0:
		case r == utf8.RuneError:
		case r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
