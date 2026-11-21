//go:build unix

package main

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/tui/client"
)

// The list is started with how to attach: the same client, the config's
// [attach], whether it runs inside tmux, and the command a tmux window
// runs, with the settings of this program's environment that command
// needs too.
func TestListIsStartedWithAttach(t *testing.T) {
	got := setup(t, twoProfiles+"\n[attach]\nmode = \"window\"\nprefer = [\"ssh\"]\n\n[attach.kubectl]\ncontext = \"lab\"\n", "prod")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--profile", "prod"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	o := got()
	if o == nil || o.Attach == nil {
		t.Fatalf("the list was started without attach: %+v", o)
	}
	at := o.Attach
	if c, ok := at.API.(*client.Client); !ok || c != o.API.(*client.Client) {
		t.Errorf("attach's API = %#v, want the list's client", at.API)
	}
	if at.Mode != "window" || !at.InTmux {
		t.Errorf("mode %q, in tmux %v; want window, true", at.Mode, at.InTmux)
	}
	if !slices.Equal(at.Config.Prefer, []string{"ssh"}) || at.Config.KubectlContext != "lab" {
		t.Errorf("config = %+v, want the file's [attach]", at.Config)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{exe, "attach", "--profile", "prod", "--keep-open"}; !slices.Equal(at.Window, want) {
		t.Errorf("the window's command = %q, want %q", at.Window, want)
	}
	// Each setting is given, also one that is unset here: the tmux
	// server's own environment may have another value for it, which the
	// window must not use.
	want := []string{"LOOMUX_SERVER=", "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME")}
	if !slices.Equal(at.WindowEnv, want) {
		t.Errorf("the window's environment = %q, want %q", at.WindowEnv, want)
	}

	// Outside tmux, and with the server given in the environment: the
	// window's command must reach the same server.
	got = setup(t, twoProfiles, "test")
	t.Setenv("TMUX", "")
	t.Setenv("LOOMUX_SERVER", "https://loomux.example.net")
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	at = got().Attach
	if at.InTmux || at.Mode != "auto" {
		t.Errorf("mode %q, in tmux %v; want auto, false", at.Mode, at.InTmux)
	}
	if !slices.Contains(at.WindowEnv, "LOOMUX_SERVER=https://loomux.example.net") {
		t.Errorf("the window's environment = %q, want LOOMUX_SERVER in it", at.WindowEnv)
	}
}

// --keep-open is for a tmux window, which closes with its command: after
// a failure the reason stays on screen until enter.
func TestAttachKeepOpen(t *testing.T) {
	e := newAttachEnv(t) // no ssh on PATH: the attach fails
	typing, type_ := io.Pipe()
	defer type_.Close()
	old := stdin
	stdin = typing
	defer func() { stdin = old }()

	var stderr lockedBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"attach", "--keep-open", "--task", e.task.ID}, io.Discard, &stderr) }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(stderr.String(), "enter") {
		if time.Now().After(deadline) {
			t.Fatalf("no word about enter:\n%s", stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case code := <-done:
		t.Fatalf("exited %d without waiting for enter:\n%s", code, stderr.String())
	case <-time.After(100 * time.Millisecond):
	}
	if !strings.Contains(stderr.String(), "PATH") {
		t.Errorf("the reason isn't on screen:\n%s", stderr.String())
	}
	if _, err := type_.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("didn't exit after enter")
	}

	// An attach that worked closes at once.
	e.ssh("exit 0")
	e.stdin("")
	code, _, out := loomux("attach", "--keep-open", "--task", e.task.ID)
	if code != 0 || strings.Contains(out, "enter") {
		t.Errorf("after a detach: exit %d, stderr:\n%s", code, out)
	}
}
