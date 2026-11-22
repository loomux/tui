//go:build unix

package attach

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// pollLine is readLine for a file: it waits for input with poll and
// reads only what is there, so when ctx ends nothing is left reading the
// file. A terminal in line mode has input once a line is entered.
func pollLine(ctx context.Context, f *os.File) (line []byte, ok, polled bool) {
	fd := int32(f.Fd())
	ready := func() bool {
		for ctx.Err() == nil {
			fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
			n, err := unix.Poll(fds, int(pollEvery/time.Millisecond))
			if err != nil && !errors.Is(err, unix.EINTR) {
				return true // let the read say what is wrong
			}
			if n > 0 {
				return true
			}
		}
		return false
	}
	cancelled := false
	line, ok = scanLine(func(b []byte) (int, error) {
		if !ready() {
			cancelled = true
			return 0, context.Canceled
		}
		return f.Read(b)
	})
	if cancelled {
		return nil, false, true
	}
	return line, ok, true
}

// pollEvery is how often a waiting pollLine looks at its context.
const pollEvery = 50 * time.Millisecond
