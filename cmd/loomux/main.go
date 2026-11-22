// Command loomux is the terminal client for Loomux.
//
// With no command it shows the attention-first list of conversations for
// the selected profile, from which a conversation's agent can be attached
// to, and open ID shows it on that conversation (the web's "open in
// terminal"). version prints the client's version, and attach attaches
// this terminal to an agent's tmux session under a hold. Login, logout
// and the other screens come in later tickets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/attach"
	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config"
	"github.com/Loomux/tui/internal/version"
	"github.com/Loomux/tui/ui"
)

const usage = `usage: loomux [--profile NAME]
       loomux [--profile NAME] open ID
       loomux version
       loomux attach [--profile NAME] [--force] [--keep-open] <conversation|task>
       loomux attach [--profile NAME] [--force] [--keep-open] --task ID

  (no command)  the list of conversations, on the profile's server
  open ID       the list, on conversation ID (the web's "open in terminal")
  version       print the client version and the server release it is pinned to
  attach        attach this terminal to a conversation's agent, in its tmux
                session, and hand the agent back when you detach. Meanwhile
                the server knows who is in the pane, and types nothing
                into it.

  --profile NAME  the config's profile to use (else LOOMUX_PROFILE, else
                  default_profile); LOOMUX_SERVER overrides its server URL
  --force         take the task from another client that holds it,
                  without asking
  --task ID       the id is a task's (otherwise a conversation's is
                  tried first)
  --keep-open     after a failure, wait for enter before exiting: for a
                  window that closes with the command, as the list's
                  attach opens inside tmux
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// runUI runs the screens until they quit; tests replace it.
var runUI = func(o ui.Options) (err error) {
	app := ui.New(o)
	// A hold taken for an attach the screens ended before is given back. A
	// release that failed where the screens couldn't say so is the error.
	defer func() { err = errors.Join(err, app.Close()) }()
	_, err = tea.NewProgram(app).Run()
	return err
}

// run executes one invocation and returns its exit status: 0 on success,
// 1 when it fails, 2 for a usage error, and attach's own (attachMain).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "loomux %s\nserver pin %s\n", version.Client(), version.ServerPin)
		return 0
	}
	if len(args) > 0 && args[0] == "attach" {
		return attachMain(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("loomux", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var open string
	switch rest := fs.Args(); {
	case len(rest) == 0:
	case rest[0] == "open":
		// --profile may come after open too.
		ofs := flag.NewFlagSet("open", flag.ContinueOnError)
		ofs.SetOutput(io.Discard)
		ofs.StringVar(profile, "profile", *profile, "")
		if err := ofs.Parse(rest[1:]); err != nil || ofs.NArg() != 1 || strings.TrimSpace(ofs.Arg(0)) == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		open = strings.TrimSpace(ofs.Arg(0))
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err := list(*profile, open, stderr); err != nil {
		fmt.Fprintf(stderr, "loomux: %v\n", err)
		return 1
	}
	return 0
}

// list shows the list of conversations on the profile's server, signed
// in with the profile's stored token, and goes to conversation open when
// it isn't "".
func list(profile, open string, stderr io.Writer) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	p, err := cfg.Select(profile, os.Getenv)
	if err != nil {
		return err
	}
	store, err := auth.Open(cfg)
	if err != nil {
		return err
	}
	account := auth.AccountFor(p)
	if w := auth.Warning(store, account); w != "" {
		fmt.Fprintln(stderr, w)
	}
	token, err := store.Get(account)
	if errors.Is(err, auth.ErrNotFound) {
		return fmt.Errorf("not signed in to profile %q (%s): sign in with loomux login", p.Name, p.Server)
	}
	if err != nil {
		return err
	}
	c, err := client.New(p.Server, token)
	if err != nil {
		return err
	}
	return runUI(ui.Options{Profile: p.Name, API: c, Keys: cfg.Keymap, Attach: attachFromList(c, cfg, p), Server: p.Server, Open: open})
}

// attachFromList is how the list's attach key attaches: the config's
// [attach], and, for a tmux window of its own, this program run as
// `loomux attach --profile NAME --keep-open` (the list adds the task).
func attachFromList(c *client.Client, cfg *config.Config, p config.Profile) *ui.Attach {
	at := &ui.Attach{
		API:    c,
		Config: attach.Config{Prefer: cfg.Attach.Prefer, KubectlContext: cfg.Attach.Kubectl.Context},
		Mode:   cfg.Attach.Mode,
		InTmux: os.Getenv("TMUX") != "",
	}
	// Without this program's own path there is no window to open: the
	// attach is then on this terminal.
	if exe, err := os.Executable(); err == nil {
		at.Window = []string{exe, "attach", "--profile", p.Name, "--keep-open"}
	}
	at.WindowEnv = windowEnv(os.Getenv)
	return at
}

// windowEnv is the part of this program's environment that a tmux window
// attaching for the list gets by name. A tmux window starts from the tmux
// server's environment, from wherever that was started, not from this
// program's; with these the window attaches as the list would here:
//
//   - what chooses the server, the config file and the token store
//     (LOOMUX_SERVER, the XDG directories, HOME, and the session bus the
//     keyring is reached on);
//   - where the attach commands are found (PATH) and where they go
//     (kubectl's KUBECONFIG, docker's DOCKER_HOST, DOCKER_CONTEXT and
//     DOCKER_CONFIG).
//
// One of the first group of names that is unset here is given empty,
// which each of them reads as unset, so the tmux server's own value isn't
// used. The others are given only when set: an empty PATH isn't an unset
// one.
//
// The rest stays tmux's: the terminal and the tmux the window is in, the
// ssh agent (tmux keeps it current for its sessions), and everything
// else. The whole environment isn't passed: tmux bounds the size of a
// command.
func windowEnv(getenv func(string) string) []string {
	var env []string
	for _, name := range []string{
		"LOOMUX_SERVER", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "DBUS_SESSION_BUS_ADDRESS",
		"KUBECONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	} {
		env = append(env, name+"="+getenv(name))
	}
	for _, name := range []string{"HOME", "PATH", "XDG_RUNTIME_DIR"} {
		if v := getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}
