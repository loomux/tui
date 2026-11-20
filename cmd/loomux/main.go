// Command loomux is the terminal client for Loomux.
//
// With no command it shows the attention-first list of conversations for
// the selected profile, from which a conversation's agent can be attached
// to. version prints the client's version, and attach attaches this
// terminal to an agent's tmux session under a hold. Login, logout, open
// and the other screens come in later tickets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/attach"
	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config"
	"github.com/Loomux/tui/internal/version"
	"github.com/Loomux/tui/ui"
)

const usage = `usage: loomux [--profile NAME]
       loomux version
       loomux attach [--profile NAME] [--force] [--keep-open] <conversation|task>
       loomux attach [--profile NAME] [--force] [--keep-open] --task ID

  (no command)  the list of conversations, on the profile's server
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
var runUI = func(o ui.Options) error {
	app := ui.New(o)
	// A hold taken for an attach the screens ended before is given back.
	defer app.Close()
	_, err := tea.NewProgram(app).Run()
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
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err := list(*profile, stderr); err != nil {
		fmt.Fprintf(stderr, "loomux: %v\n", err)
		return 1
	}
	return 0
}

// list shows the list of conversations on the profile's server, signed
// in with the profile's stored token.
func list(profile string, stderr io.Writer) error {
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
	return runUI(ui.Options{Profile: p.Name, API: c, Keys: cfg.Keymap, Attach: attachFromList(c, cfg, p)})
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
	// A tmux window starts from the tmux server's environment, not this
	// program's: what chooses the config, the token store and the server
	// here has to reach it by name.
	for _, name := range []string{"LOOMUX_SERVER", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if v := os.Getenv(name); v != "" {
			at.WindowEnv = append(at.WindowEnv, name+"="+v)
		}
	}
	return at
}
