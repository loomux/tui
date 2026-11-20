// Command loomux is the terminal client for Loomux.
//
// With no command it shows the attention-first list of conversations for
// the selected profile, and open ID shows it on that conversation (the
// web's "open in terminal"). version prints the client's version. Login,
// logout, attach and the other screens come in later tickets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config"
	"github.com/Loomux/tui/internal/version"
	"github.com/Loomux/tui/ui"
)

const usage = `usage: loomux [--profile NAME]
       loomux [--profile NAME] open ID
       loomux version

  (no command)  the list of conversations, on the profile's server
  open ID       the list, on conversation ID (the web's "open in terminal")
  version       print the client version and the server release it is pinned to

  --profile NAME  the config's profile to use (else LOOMUX_PROFILE, else
                  default_profile); LOOMUX_SERVER overrides its server URL
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// runUI runs the screens until they quit; tests replace it.
var runUI = func(o ui.Options) error {
	_, err := tea.NewProgram(ui.New(o)).Run()
	return err
}

// run executes one invocation and returns its exit status: 0 on success,
// 1 when it fails, 2 for a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "loomux %s\nserver pin %s\n", version.Client(), version.ServerPin)
		return 0
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
	return runUI(ui.Options{Profile: p.Name, API: c, Keys: cfg.Keymap, Server: p.Server, Open: open})
}
