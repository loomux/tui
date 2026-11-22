package attach

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal: its terminal side for the command,
// and the other side, which reads what was written to the terminal.
func openPTY(t *testing.T) (terminal, master *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminal.Close() })
	return terminal, master
}

func termios(t *testing.T, f *os.File) unix.Termios {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return *tio
}

// A command that dies with the terminal in raw mode, on the alternate
// screen, leaves it as the client found it: line settings back, the
// screen and its modes reset.
func TestRunRestoresTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		script    string
		wantReset bool
	}{
		{"a command that failed", `stty raw -echo; printf '\033[?1049h\033[?1000h'; exit 3`, true},
		{"a command that was killed", `stty raw -echo; kill -KILL $$`, true},
		{"a command that ended cleanly", `stty raw -echo; exit 0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.program("ssh", "exit 0")
			terminal, master := openPTY(t)
			written := make(chan []byte, 1)
			go func() {
				b, _ := io.ReadAll(master) // until the terminal side is closed
				written <- b
			}()
			before := termios(t, terminal)
			if before.Lflag&unix.ECHO == 0 || before.Lflag&unix.ICANON == 0 {
				t.Fatalf("the new terminal isn't in line mode: %+v", before)
			}

			s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
			if err != nil {
				t.Fatal(err)
			}
			s.Plan.Commands = []Command{w.program("kubectl", tc.script)}
			_, err = s.Run(ctx(t), Terminal{In: terminal, Out: terminal, Err: terminal})
			var failed *FailedError
			if tc.wantReset && !errors.As(err, &failed) {
				t.Errorf("err = %v, want a FailedError", err)
			}
			if !tc.wantReset && err != nil {
				t.Errorf("err = %v", err)
			}

			if after := termios(t, terminal); after != before {
				t.Errorf("the terminal's settings weren't restored:\n got %+v\nwant %+v", after, before)
			}
			terminal.Close()
			out := <-written
			if got := bytes.Contains(out, []byte(resetModes)); got != tc.wantReset {
				t.Errorf("modes reset = %v, want %v; the terminal got %q", got, tc.wantReset, out)
			}
			if h := w.srv.ActiveHold(w.task.ID); h != nil {
				t.Errorf("still held: %+v", h)
			}
		})
	}
}

// The escape sequences only go to a terminal.
func TestRunWritesNoEscapesToAFile(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("kubectl", "exit 3")}
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := s.Run(ctx(t), Terminal{Out: out, Err: out}); err == nil {
		t.Error("Run: no error for a command that failed")
	}
	if b := readFile(t, out.Name()); strings.Contains(b, "\x1b") {
		t.Errorf("an escape sequence went to a file: %q", b)
	}
}

// The design's "Crashed TUI": kill -9 of the client hangs its attach
// command up, without waiting for the server to detach it.
func TestCommandIsHungUpWhenTheClientDies(t *testing.T) {
	if dir := os.Getenv("LOOMUX_TEST_ATTACH_THEN_HANG"); dir != "" {
		// The client: start the command as an attach does, and hang.
		_, err := startProcess(Command{Via: "ssh", Argv: []string{"ssh"}, Path: filepath.Join(dir, "ssh")}, Terminal{})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		select {}
	}

	dir := t.TempDir()
	// The command outlives a parent that doesn't hang it up, for 30 s:
	// long enough to fail the test, not to be left behind.
	script := "#!/bin/sh\nPATH=/usr/bin:/bin\ntrap 'touch " + dir + "/hung-up; exit 0' HUP\ntouch " + dir + "/started\n" +
		"i=0; while [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done\ntouch " + dir + "/outlived\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := exec.Command(os.Args[0], "-test.run=^TestCommandIsHungUpWhenTheClientDies$")
	client.Env = append(os.Environ(), "LOOMUX_TEST_ATTACH_THEN_HANG="+dir)
	client.Stderr = os.Stderr
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Process.Kill()
		_ = client.Wait()
	}()
	waitFor(t, "the command to start", exists(filepath.Join(dir, "started")))
	// It isn't hung up while the client lives.
	time.Sleep(200 * time.Millisecond)
	if exists(filepath.Join(dir, "hung-up"))() {
		t.Fatal("the command was hung up while its client was alive")
	}
	if err := client.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command to be hung up", exists(filepath.Join(dir, "hung-up")))
}
