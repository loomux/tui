package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config"
	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/internal/version"
	"github.com/Loomux/tui/ui"
)

// TestMain is the package's for every test file.
//
// No test reaches the real keyring: the tests' configs name the file
// store, and the ones about the keyring get go-keyring's in-memory mock
// (login and logout, auth_test.go). That holds for the runs below too.
//
// The test binary can stand in for loomux itself: started with
// LOOMUX_TEST_RUN, it runs that invocation with the real standard streams
// and exits with its status. The terminal tests start it that way in a
// tmux pane, so what attaches is the client's own main, on a terminal
// (attach_tmux_test.go).
func TestMain(m *testing.M) {
	keyring.MockInit()
	// Started with arguments that aren't the test binary's own: the tmux
	// window the screens open runs this program with "attach ...".
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	if args, ok := os.LookupEnv("LOOMUX_TEST_RUN"); ok {
		if pidfile := os.Getenv("LOOMUX_TEST_PIDFILE"); pidfile != "" {
			if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		os.Exit(run(strings.Fields(args), os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	want := "loomux " + version.Client() + "\nserver pin " + version.ServerPin + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"-h"}, {"--version"}, {"--profile"}, {"--profile", "test", "extra"}, {"--nope"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("%q: stdout = %q, want empty", args, stdout.String())
		}
		if !strings.HasPrefix(stderr.String(), "usage: loomux") {
			t.Errorf("%q: stderr = %q, want usage", args, stderr.String())
		}
	}
}

// setup writes conf as the config file and stores a token for each of
// the profiles in tokens (the config's token_store is the file). The
// list doesn't start: started returns the options it would have started
// with, or nil.
func setup(t *testing.T, conf string, tokens ...string) (started func() *ui.Options) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("LOOMUX_PROFILE", "")
	t.Setenv("LOOMUX_SERVER", "")
	if err := os.MkdirAll(filepath.Join(dir, "config", "loomux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "loomux", "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range tokens {
		state, err := auth.StateDir()
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Parse([]byte(conf))
		if err != nil {
			t.Fatal(err)
		}
		p, err := cfg.Select(name, func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if err := auth.NewFileStore(state).Set(auth.AccountFor(p), "token-"+name); err != nil {
			t.Fatal(err)
		}
	}
	var got *ui.Options
	old := runUI
	t.Cleanup(func() { runUI = old })
	runUI = func(o ui.Options) error {
		got = &o
		return nil
	}
	return func() *ui.Options { return got }
}

const twoProfiles = `
default_profile = "test"
token_store = "file"

[profiles.test]
server = "https://loomux.example.net"

[profiles.prod]
server = "https://loomux.example.org"

[keys]
attach = "A"
`

// No command starts the list, for the profile the config selects, with
// its stored token and the effective keymap.
func TestNoCommandStartsTheList(t *testing.T) {
	got := setup(t, twoProfiles, "test")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	o := got()
	if o == nil {
		t.Fatal("the list wasn't started")
	}
	if o.Profile != "test" {
		t.Errorf("profile %q, want test", o.Profile)
	}
	c, ok := o.API.(*client.Client)
	if !ok || c.Server() != "https://loomux.example.net" {
		t.Errorf("API = %#v, want a client of the test profile's server", o.API)
	}
	if o.Store == nil || o.Account.Profile != "test" || o.Account.Server != "https://loomux.example.net" || o.Token != "token-test" {
		t.Error("the list wasn't given the profile's stored token for its 401 rule")
	}
	if o.Keys == nil || o.Keys.Keys(keymap.Attach)[0] != "A" {
		t.Errorf("keys = %v, want the config's keymap", o.Keys)
	}
	if !strings.Contains(stderr.String(), "plain file") {
		t.Errorf("stderr = %q, want the file store's warning", stderr.String())
	}
}

// --profile chooses the profile, as LOOMUX_PROFILE does.
func TestProfileFlag(t *testing.T) {
	for _, args := range [][]string{{"--profile", "prod"}, {"--profile=prod"}} {
		got := setup(t, twoProfiles, "prod")
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%q: exit %d, stderr %q", args, code, stderr.String())
		}
		if o := got(); o == nil || o.Profile != "prod" || o.API.(*client.Client).Server() != "https://loomux.example.org" {
			t.Errorf("%q: started with %+v", args, o)
		}
	}
	got := setup(t, twoProfiles, "prod")
	t.Setenv("LOOMUX_PROFILE", "prod")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("LOOMUX_PROFILE: exit %d, stderr %q", code, stderr.String())
	}
	if o := got(); o == nil || o.Profile != "prod" {
		t.Errorf("LOOMUX_PROFILE: started with %+v", o)
	}
}

// Without a stored token the list isn't started: the error says how to
// sign in.
func TestNoCommandNotSignedIn(t *testing.T) {
	got := setup(t, twoProfiles)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got() != nil {
		t.Error("the list was started")
	}
	for _, w := range []string{`"test"`, "loomux.example.net", "loomux login"} {
		if !strings.Contains(stderr.String(), w) {
			t.Errorf("stderr %q lacks %s", stderr.String(), w)
		}
	}
}

// A config or profile error stops the run before the list starts.
func TestNoCommandConfigErrors(t *testing.T) {
	for name, c := range map[string]struct {
		conf string
		args []string
		want string
	}{
		"no profile":      {"token_store = \"file\"\n", nil, "no profile"},
		"unknown profile": {twoProfiles, []string{"--profile", "staging"}, `"staging"`},
		"bad key":         {twoProfiles + "watch = \"A\"\n", nil, "keys"},
	} {
		got := setup(t, c.conf)
		var stdout, stderr bytes.Buffer
		if code := run(c.args, &stdout, &stderr); code != 1 {
			t.Errorf("%s: exit %d, want 1", name, code)
		}
		if got() != nil || !strings.Contains(stderr.String(), c.want) || !strings.HasPrefix(stderr.String(), "loomux: ") {
			t.Errorf("%s: stderr %q, want %s", name, stderr.String(), c.want)
		}
	}
}

// The list's own failure is the run's.
func TestNoCommandUIError(t *testing.T) {
	setup(t, twoProfiles, "test")
	runUI = func(ui.Options) error { return errors.New("no terminal") }
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no terminal") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

// loomux open <id> starts the list on that conversation, for the
// profile's server, which o opens the web at. --profile goes before or
// after open.
func TestOpenCommand(t *testing.T) {
	for _, c := range []struct {
		args          []string
		profile, serv string
	}{
		{[]string{"open", "conv-1"}, "test", "https://loomux.example.net"},
		{[]string{"--profile", "prod", "open", "conv-1"}, "prod", "https://loomux.example.org"},
		{[]string{"open", "--profile", "prod", "conv-1"}, "prod", "https://loomux.example.org"},
		{[]string{"open", "--profile=prod", "conv-1"}, "prod", "https://loomux.example.org"},
	} {
		got := setup(t, twoProfiles, "test", "prod")
		var stdout, stderr bytes.Buffer
		if code := run(c.args, &stdout, &stderr); code != 0 {
			t.Fatalf("%q: exit %d, stderr %q", c.args, code, stderr.String())
		}
		o := got()
		if o == nil || o.Open != "conv-1" || o.Profile != c.profile || o.Server != c.serv ||
			o.API.(*client.Client).Server() != c.serv {
			t.Errorf("%q: started with %+v", c.args, o)
		}
	}
}

// With no command the list knows its server too (o opens the web there)
// and opens nothing.
func TestNoCommandServer(t *testing.T) {
	got := setup(t, twoProfiles, "test")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if o := got(); o == nil || o.Server != "https://loomux.example.net" || o.Open != "" {
		t.Errorf("started with %+v", o)
	}
}

// open takes exactly one id.
func TestOpenUsage(t *testing.T) {
	for _, args := range [][]string{{"open"}, {"open", "a", "b"}, {"open", ""}, {"open", " "}, {"open", "--nope", "x"}, {"open", "--profile"}} {
		got := setup(t, twoProfiles, "test")
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if got() != nil || !strings.HasPrefix(stderr.String(), "usage: loomux") {
			t.Errorf("%q: stderr %q", args, stderr.String())
		}
	}
}

// open needs a stored token, as the list does.
func TestOpenNotSignedIn(t *testing.T) {
	got := setup(t, twoProfiles)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"open", "conv-1"}, &stdout, &stderr); code != 1 || got() != nil ||
		!strings.Contains(stderr.String(), "loomux login") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}
