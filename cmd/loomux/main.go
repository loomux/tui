// Command loomux is the terminal client for Loomux.
//
// Only the version subcommand exists so far; login, logout, attach, open
// and the TUI itself come in later tickets.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Loomux/tui/internal/version"
)

const usage = `usage: loomux version

  version   print the client version and the server release it is pinned to
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one invocation and returns its exit status: 0 on success,
// 2 for a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "loomux %s\nserver pin %s\n", version.Client(), version.ServerPin)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}
