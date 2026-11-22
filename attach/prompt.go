package attach

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Loomux/tui/client"
)

// Ask puts a yes/no question and reads one line of answer: y or yes,
// n or no, or nothing for def. Anything else is a no, and so is no
// answer at all: the input is gone, or ctx was cancelled (a signal, or a
// question no longer worth asking) while it stood. It reads its line and
// nothing after it: the rest is the next command's.
//
// Once ctx has ended, Ask no longer reads a terminal (or any file on
// Unix): the screens that resume after it get every key.
func Ask(ctx context.Context, in io.Reader, out io.Writer, question string, def bool) bool {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Fprint(out, question+hint)
	line, ok := readLine(ctx, in)
	if !ok {
		if ctx.Err() != nil {
			fmt.Fprintln(out) // what comes next starts on its own line
		}
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(line))) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// Pause shows text and waits for a line of input (enter), for the input
// to end, or for ctx: something to be read before what shows it goes
// away.
func Pause(ctx context.Context, in io.Reader, out io.Writer, text string) {
	fmt.Fprint(out, text+" ")
	readLine(ctx, in)
	if ctx.Err() != nil {
		fmt.Fprintln(out)
	}
}

// maxAnswer bounds a line of answer.
const maxAnswer = 256

// readLine reads a line of in, without its line break. It reports false
// for no answer: the input ended with nothing on the line, the line is
// too long, or ctx ended first.
func readLine(ctx context.Context, in io.Reader) ([]byte, bool) {
	if f, isFile := in.(*os.File); isFile {
		if line, ok, polled := pollLine(ctx, f); polled {
			return line, ok
		}
	}
	// A read that can't be interrupted gets a goroutine of its own, left
	// behind if ctx ends first.
	type answer struct {
		line []byte
		ok   bool
	}
	got := make(chan answer, 1)
	go func() {
		line, ok := scanLine(func(b []byte) (int, error) { return in.Read(b) })
		got <- answer{line, ok}
	}()
	select {
	case a := <-got:
		return a.line, a.ok
	case <-ctx.Done():
		return nil, false
	}
}

// scanLine reads a line a byte at a time, so that nothing after it is
// taken from the input.
func scanLine(read func([]byte) (int, error)) ([]byte, bool) {
	var line []byte
	for b := make([]byte, 1); ; {
		n, err := read(b)
		if n == 1 {
			if b[0] == '\n' {
				return line, true
			}
			line = append(line, b[0])
		}
		if len(line) > maxAnswer || (err != nil && len(line) == 0) {
			return nil, false
		}
		if err != nil {
			return line, true
		}
	}
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
