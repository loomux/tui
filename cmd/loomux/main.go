// Command loomux is the terminal client for Loomux.
//
// login and logout sign a profile in and out, and version prints the
// client's version. Attach, open and the TUI itself come in later
// tickets.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Loomux/tui/internal/version"
)

const usage = `usage: loomux login [--profile NAME] [--no-browser]
       loomux logout [--profile NAME]
       loomux version

  login     sign in: approve this terminal from a browser that is signed in
            to Loomux, by a link and a code to compare
  logout    sign out: end the session on the server and delete its token
  version   print the client version and the server release it is pinned to

  --profile NAME  the config's profile to use (else LOOMUX_PROFILE, else
                  default_profile); LOOMUX_SERVER overrides its server URL
  --no-browser    print the link without opening a browser
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one invocation and returns its exit status: 0 on success,
// 1 when it fails, 2 for a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "loomux %s\nserver pin %s\n", version.Client(), version.ServerPin)
		return 0
	}
	// --profile may come before the command or after it.
	fs := flag.NewFlagSet("loomux", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "")
	if err := fs.Parse(args); err == nil && fs.NArg() > 0 {
		switch fs.Arg(0) {
		case "login":
			return login(*profile, fs.Args()[1:], stdout, stderr)
		case "logout":
			return logout(*profile, fs.Args()[1:], stdout, stderr)
		}
	}
	fmt.Fprint(stderr, usage)
	return 2
}
