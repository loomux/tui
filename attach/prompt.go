package attach

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Loomux/tui/client"
)

// Ask puts a yes/no question and reads one line of answer: y or yes,
// n or no, or nothing for def. Anything else is a no, and so is no
// answer at all: the input is gone, or ctx was cancelled (a signal)
// while the question stood. It reads its line and nothing after it: the
// rest is the next command's.
func Ask(ctx context.Context, in io.Reader, out io.Writer, question string, def bool) bool {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Fprint(out, question+hint)
	// A read of a terminal can't be interrupted, so it gets a goroutine
	// of its own, left behind if ctx ends first: the client is then on
	// its way out.
	answer := make(chan bool, 1)
	go func() { answer <- readAnswer(in, def) }()
	select {
	case yes := <-answer:
		return yes
	case <-ctx.Done():
		fmt.Fprintln(out)
		return false
	}
}

func readAnswer(in io.Reader, def bool) bool {
	var line []byte
	for b := make([]byte, 1); ; {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if len(line) > 256 || (err != nil && len(line) == 0) {
			return false
		}
		if err != nil {
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(line))) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// HeldLine names who holds a task.
func HeldLine(h *client.Hold) string {
	if h == nil {
		return "the task is held by another client"
	}
	from := "another client"
	switch h.ClientKind {
	case "tui":
		from = "the terminal"
	case "web":
		from = "the web"
	case "app":
		from = "the app"
	}
	line := "the task is being driven from " + from
	if label := Clean(h.ClientLabel); label != "" {
		line += " on " + label
	}
	if !h.AcquiredAt.IsZero() {
		line += " since " + h.AcquiredAt.Local().Format("15:04")
	}
	return line
}
