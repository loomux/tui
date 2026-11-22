package attach

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// process is a running attach command.
type process struct {
	cmd *exec.Cmd
	// done is closed when the command has ended; err is then how
	// (exec.Cmd.Wait's).
	done chan struct{}
	err  error
}

// startProcess starts the command on the terminal: the argv as it is,
// its program the file found on PATH when it was chosen. No shell.
//
// It is started from a goroutine locked to its OS thread, which then
// waits for it: on Linux the command gets a hangup when its parent dies
// (sysProcAttr), and "parent" there is the thread that started it, not
// the process. A thread the Go runtime retired would hang the command up
// while the client lives.
func startProcess(c Command, t Terminal) (*process, error) {
	p := &process{done: make(chan struct{})}
	started := make(chan error)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		cmd := &exec.Cmd{
			Path: c.Path, Args: c.Argv, Env: commandEnv(c),
			Stdin: t.In, Stdout: t.Out, Stderr: t.Err,
			SysProcAttr: sysProcAttr(),
			// Only for streams that aren't files (a test's): don't wait
			// for a grandchild that kept one open.
			WaitDelay: time.Second,
		}
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		p.cmd = cmd
		started <- nil
		p.err = cmd.Wait()
		close(p.done)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return p, nil
}

// commandEnv is the command's environment: the client's own. A local
// target's tmux is another tmux server than the one this terminal may be
// inside, and tmux refuses to attach from inside tmux while TMUX is set,
// so that one command runs without it.
func commandEnv(c Command) []string {
	if c.Via != ViaLocal {
		return nil // the client's own
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	return env
}

// end ends the command: a hangup, which a tmux client answers by
// detaching and ssh by closing its session, and a kill if it is still
// there after wait.
func (p *process) end(wait time.Duration) {
	_ = p.cmd.Process.Signal(syscall.SIGHUP)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case <-timer.C:
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

// resetModes undoes what a tmux that didn't get to clean up (an ssh that
// dropped, a command that was killed) leaves set on the terminal: the
// alternate screen, mouse and focus reporting, bracketed paste, a hidden
// cursor, application cursor keys and keypad, colours and the
// line-drawing character set. Each is a no-op on a terminal that isn't
// in that mode. The alternate screen is left with 1047, not 1049: 1049
// also restores a saved cursor position, which moves the cursor on a
// terminal that never entered it.
const resetModes = "\x1b[?1047l" +
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l" +
	"\x1b[?25h\x1b[?1l\x1b>" +
	"\x1b[0m\x1b(B"

// saveTerminal notes the terminal's settings before a command gets it,
// and returns what puts them back when the command has ended: always the
// line settings (a command killed in raw mode leaves no echo and no line
// editing), and, after a command that didn't end cleanly, the modes of
// resetModes. Streams that aren't a terminal are left alone.
func saveTerminal(t Terminal) (restore func(unclean bool)) {
	var state *term.State
	in, _ := t.In.(*os.File)
	if in != nil && term.IsTerminal(int(in.Fd())) {
		state, _ = term.GetState(int(in.Fd()))
	}
	return func(unclean bool) {
		if state != nil {
			_ = term.Restore(int(in.Fd()), state)
		}
		if out, ok := t.Out.(*os.File); unclean && ok && term.IsTerminal(int(out.Fd())) {
			_, _ = out.WriteString(resetModes)
		}
	}
}
