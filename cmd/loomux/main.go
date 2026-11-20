// Command loomux is the terminal client for Loomux.
//
// version prints the client's version, and attach attaches this terminal
// to an agent's tmux session under a hold. Login, logout, open and the
// screens come in later tickets.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Loomux/tui/internal/version"
)

const usage = `usage: loomux version
       loomux attach [--profile NAME] [--force] <conversation|task>
       loomux attach [--profile NAME] [--force] --task ID

  version  print the client version and the server release it is pinned to
  attach   attach this terminal to a conversation's agent, in its tmux
           session, and hand the agent back when you detach. Meanwhile
           the server knows who is in the pane, and types nothing into it.

  --profile NAME  the config's profile to use (else LOOMUX_PROFILE, else
                  default_profile); LOOMUX_SERVER overrides its server URL
  --force         take the task from another client that holds it,
                  without asking
  --task ID       the id is a task's (otherwise a conversation's is
                  tried first)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one invocation and returns its exit status: 0 on success,
// 2 for a usage error, and attach's own (attachMain).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "loomux %s\nserver pin %s\n", version.Client(), version.ServerPin)
		return 0
	}
	if len(args) > 0 && args[0] == "attach" {
		return attachMain(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return 2
}
