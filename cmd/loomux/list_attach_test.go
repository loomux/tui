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
	// The settings that choose the server, the config and the token store
	// are given, also one that is unset here: the tmux server's own
	// environment may have another value for it, which the window must
	// not use.
	for _, want := range []string{"LOOMUX_SERVER=", "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME")} {
		if !slices.Contains(at.WindowEnv, want) {
			t.Errorf("the window's environment %q has no %q", at.WindowEnv, want)
		}
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

// The window attaches with the list's settings: what chooses the server,
// the config and the token store, and where the attach commands are
// found and go. The rest of its environment is tmux's.
func TestWindowEnv(t *testing.T) {
	env := map[string]string{
		"HOME":            "/home/someone",
		"PATH":            "/opt/tools/bin:/usr/bin",
		"LOOMUX_SERVER":   "https://loomux.example.net",
		"XDG_CONFIG_HOME": "/home/someone/cfg",
		"KUBECONFIG":      "/home/someone/.kube/lab",
		"DOCKER_HOST":     "ssh://box.example.net",
		"TERM":            "xterm-256color",
		"TMUX":            "/tmp/tmux-1000/default,1,0",
		"SSH_AUTH_SOCK":   "/tmp/ssh-agent.sock",
		"LS_COLORS":       "di=01;34",
	}
	got := windowEnv(func(name string) string { return env[name] })
	for _, want := range []string{
		"HOME=/home/someone", "PATH=/opt/tools/bin:/usr/bin",
		"LOOMUX_SERVER=https://loomux.example.net", "XDG_CONFIG_HOME=/home/someone/cfg",
		"KUBECONFIG=/home/someone/.kube/lab", "DOCKER_HOST=ssh://box.example.net",
		// Unset here: given empty, which each of these reads as unset.
		"XDG_STATE_HOME=", "DOCKER_CONTEXT=", "DOCKER_CONFIG=", "DBUS_SESSION_BUS_ADDRESS=",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		// The terminal and the tmux the window is in are tmux's to say;
		// so is the ssh agent, which tmux keeps current for its sessions.
		case "TERM", "TMUX", "TMUX_PANE", "SSH_AUTH_SOCK", "LS_COLORS":
			t.Errorf("%s is passed to the window", name)
		}
	}

	// An empty PATH or HOME isn't an unset one: unset here, they are left
	// to tmux.
	got = windowEnv(func(string) string { return "" })
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			t.Errorf("%q is passed with nothing set", kv)
		}
	}
}
