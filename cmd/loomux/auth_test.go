package main

// `loomux login` and `loomux logout` against a real api.Server from the
// pinned server module (its api/apitest). The tests play the browser:
// they take the link the command shows and approve it as the web's /link
// page does, with POST /api/v1/sessions from a logged-in session.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"
	"github.com/zalando/go-keyring"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
)

// No test reaches the real keyring: the tests' configs name the file
// store, and the ones about the keyring get go-keyring's in-memory mock.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// output is a command's stdout or stderr, read while it still writes.
type output struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

var linkPattern = regexp.MustCompile(`https?://\S+/link#\S+`)

// world is one test's machine: a config with profile "test" on a real
// server, the file token store in a temporary state directory, a host
// name, and no browser to open (opened records what would have been).
type world struct {
	srv     *apitest.Server
	account auth.Account
	store   *auth.FileStore
	opened  []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{srv: apitest.New(t)}
	w.account = auth.Account{Profile: "test", Server: w.srv.URL}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("LOOMUX_PROFILE", "")
	t.Setenv("LOOMUX_SERVER", "")
	w.store = auth.NewFileStore(filepath.Join(dir, "state", "loomux"))
	writeConfig(t, fmt.Sprintf("token_store = \"file\"\n\n[profiles.test]\nserver = %q\n", w.srv.URL))

	oldOpen, oldHost, oldSignal, oldInterval, oldTimeout := openBrowser, hostname, signalContext, linkInterval, linkTimeout
	t.Cleanup(func() {
		openBrowser, hostname, signalContext, linkInterval, linkTimeout = oldOpen, oldHost, oldSignal, oldInterval, oldTimeout
	})
	openBrowser = func(link string) bool {
		w.opened = append(w.opened, link)
		return false
	}
	hostname = func() (string, error) { return "test-host", nil }
	signalContext = func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
	linkInterval, linkTimeout = 5*time.Millisecond, 2*time.Minute
	return w
}

func writeConfig(t *testing.T, conf string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "loomux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
}

// approve is the /link page's Approve for link, by the session with
// token.
func (w *world) approve(t *testing.T, token, link string) {
	t.Helper()
	q, err := url.ParseQuery(link[strings.Index(link, "#")+1:])
	if err != nil {
		t.Errorf("the link %q: %v", link, err)
		return
	}
	body, _ := json.Marshal(map[string]string{"token_hash": q.Get("h"), "label": q.Get("label"), "client_kind": "tui"})
	req, err := http.NewRequest(http.MethodPost, w.srv.URL+"/api/v1/sessions", bytes.NewReader(body))
	if err != nil {
		t.Error(err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("approve: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("the browser's approval = %d, want 201", resp.StatusCode)
	}
}

// approveFrom approves the link once the command has printed it, as a
// user reading the terminal does. It returns when run has, with the link.
func (w *world) approveFrom(t *testing.T, stdout *output, token string, run func()) string {
	t.Helper()
	found := make(chan string, 1)
	stop := make(chan struct{})
	go func() {
		for {
			if link := linkPattern.FindString(stdout.String()); link != "" {
				w.approve(t, token, link)
				found <- link
				return
			}
			select {
			case <-stop:
				found <- ""
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	run()
	close(stop)
	return <-found
}

// current is the session the server has for token.
func (w *world) current(t *testing.T, token string) client.Session {
	t.Helper()
	c, err := client.New(w.srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ss, err := c.Sessions(ctx)
	if err != nil {
		t.Fatalf("the server doesn't take the token: %v", err)
	}
	for _, s := range ss {
		if s.Current {
			return s
		}
	}
	t.Fatal("no current session")
	return client.Session{}
}

func (w *world) wantNothingStored(t *testing.T) {
	t.Helper()
	if _, err := w.store.Get(w.account); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the token store's Get = %v, want ErrNotFound: nothing is stored", err)
	}
}

func TestLoginSignsIn(t *testing.T) {
	w := newWorld(t)
	browser := w.srv.Login()
	openBrowser = func(link string) bool {
		w.opened = append(w.opened, link)
		go w.approve(t, browser, link)
		return true
	}
	var stdout, stderr output
	if code := run([]string{"login"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	token, err := w.store.Get(w.account)
	if err != nil {
		t.Fatalf("no token stored: %v", err)
	}
	// The link is printed as well as opened, with the code of its hash.
	hash := auth.TokenHash(token)
	link := auth.LinkURL(w.srv.URL, hash, "test-host")
	if len(w.opened) != 1 || w.opened[0] != link {
		t.Errorf("the browser was given %q, want %q", w.opened, link)
	}
	out := stdout.String()
	for _, want := range []string{link, auth.LinkCode(hash), `"test"`, w.srv.URL, `"test-host"`, waitWords(linkTimeout), "ctrl+c", "your browser", "Signed in"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout doesn't say %q:\n%s", want, out)
		}
	}
	// The token is never printed.
	if strings.Contains(out, token) || strings.Contains(stderr.String(), token) {
		t.Error("the token was printed")
	}
	// The file store's warning, once.
	if got := stderr.String(); strings.Count(got, "plain file") != 1 || strings.Count(got, "\n") != 1 {
		t.Errorf("stderr = %q, want the one line about the plain file", got)
	}
	if cur := w.current(t, token); cur.Label != "test-host" || cur.ClientKind != registry.SessionKindTUI {
		t.Errorf("the session = %+v, want test-host, tui", cur)
	}
}

// --no-browser, or no browser to open: the link on the terminal is enough.
func TestLoginWithoutOpeningABrowser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		opened int
	}{
		{"--no-browser", []string{"login", "--no-browser"}, 0},
		{"no browser to open", []string{"login"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			browser := w.srv.Login()
			var stdout, stderr output
			code := -1
			link := w.approveFrom(t, &stdout, browser, func() { code = run(tc.args, &stdout, &stderr) })
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			if len(w.opened) != tc.opened {
				t.Errorf("the browser was tried %d times, want %d", len(w.opened), tc.opened)
			}
			if strings.Contains(stdout.String(), "your browser") {
				t.Errorf("stdout says a browser was opened:\n%s", stdout.String())
			}
			token, err := w.store.Get(w.account)
			if err != nil || auth.LinkURL(w.srv.URL, auth.TokenHash(token), "test-host") != link {
				t.Errorf("the stored token isn't the printed link's (%v)", err)
			}
		})
	}
}

func TestLoginLabelWithoutAHostName(t *testing.T) {
	w := newWorld(t)
	hostname = func() (string, error) { return "", errors.New("no host name") }
	linkTimeout = 50 * time.Millisecond
	var stdout, stderr output
	run([]string{"login"}, &stdout, &stderr)
	if link := linkPattern.FindString(stdout.String()); !strings.HasSuffix(link, "&label=terminal") {
		t.Errorf("link = %q, want the label \"terminal\"", link)
	}
	w.wantNothingStored(t)
}

func TestLoginCancelled(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	openBrowser = func(string) bool {
		cancel() // ctrl+c while the link waits
		return true
	}
	var stdout, stderr output
	if code := run([]string{"login"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, stderr.String())
	}
	got := stderr.String()
	for _, want := range []string{"loomux: login: cancelled", "Nothing was stored", "Undo approval"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr doesn't say %q:\n%s", want, got)
		}
	}
	if strings.Contains(stdout.String(), "Signed in") {
		t.Errorf("stdout = %q", stdout.String())
	}
	w.wantNothingStored(t)
}

func TestLoginTimesOut(t *testing.T) {
	w := newWorld(t)
	linkTimeout = 100 * time.Millisecond
	var stdout, stderr output
	if code := run([]string{"login", "--no-browser"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, stderr.String())
	}
	got := stderr.String()
	for _, want := range []string{"loomux: login: timed out waiting for the approval", "Nothing was stored", "Undo approval"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr doesn't say %q:\n%s", want, got)
		}
	}
	w.wantNothingStored(t)
}

func TestLoginAlreadySignedIn(t *testing.T) {
	w := newWorld(t)
	stored := w.srv.SeedLinkedSession("desk", registry.SessionKindTUI)
	if err := w.store.Set(w.account, stored); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr output
	if code := run([]string{"login"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Already signed in", `"test"`, `"desk"`, "loomux logout"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout doesn't say %q:\n%s", want, out)
		}
	}
	if len(w.opened) != 0 || linkPattern.MatchString(out) {
		t.Errorf("a link was shown or opened:\n%s", out)
	}
	if got, err := w.store.Get(w.account); err != nil || got != stored {
		t.Errorf("the stored token changed (%v)", err)
	}
}

// The default store is the keyring: the token goes there, and there is no
// warning.
func TestLoginStoresInTheKeyring(t *testing.T) {
	w := newWorld(t)
	writeConfig(t, fmt.Sprintf("[profiles.test]\nserver = %q\n", w.srv.URL))
	t.Cleanup(func() { _ = keyring.Delete("loomux", w.account.String()) })
	browser := w.srv.Login()
	var stdout, stderr output
	code := -1
	w.approveFrom(t, &stdout, browser, func() { code = run([]string{"login", "--no-browser"}, &stdout, &stderr) })
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	token, err := keyring.Get("loomux", "test@"+w.srv.URL)
	if err != nil {
		t.Fatalf("the keyring has no token: %v", err)
	}
	if cur := w.current(t, token); cur.Label != "test-host" {
		t.Errorf("the session = %+v", cur)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
	w.wantNothingStored(t) // not in a file as well
}

// No keyring: login refuses and says how to opt in to the file store,
// before any link.
func TestLoginWithoutAKeyring(t *testing.T) {
	w := newWorld(t)
	writeConfig(t, fmt.Sprintf("[profiles.test]\nserver = %q\n", w.srv.URL))
	keyring.MockInitWithError(errors.New("org.freedesktop.secrets was not provided by any .service files"))
	defer keyring.MockInit()
	var stdout, stderr output
	if code := run([]string{"login"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "loomux: login: ") || !strings.Contains(got, `token_store = "file"`) {
		t.Errorf("stderr = %q, want the way to the file store", got)
	}
	if stdout.String() != "" || len(w.opened) != 0 {
		t.Errorf("stdout = %q, opened %v: want nothing shown", stdout.String(), w.opened)
	}
}

// deadServer is the URL of a server that isn't there.
func deadServer(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	s.Close()
	return s.URL
}

// The profile is --profile (before or after the command), then
// LOOMUX_PROFILE, then the config's default.
func TestLoginAndLogoutProfile(t *testing.T) {
	for _, cmd := range []string{"login", "logout"} {
		for name, tc := range map[string]struct {
			args []string
			env  string
			want string
		}{
			"the default":                    {args: []string{cmd}, want: "a"},
			"--profile after":                {args: []string{cmd, "--profile", "b"}, want: "b"},
			"--profile= after":               {args: []string{cmd, "--profile=b"}, want: "b"},
			"--profile before":               {args: []string{"--profile", "b", cmd}, want: "b"},
			"the last one wins":              {args: []string{"--profile", "a", cmd, "--profile", "b"}, want: "b"},
			"LOOMUX_PROFILE":                 {args: []string{cmd}, env: "b", want: "b"},
			"--profile over the environment": {args: []string{cmd, "--profile", "a"}, env: "b", want: "a"},
		} {
			t.Run(cmd+"/"+name, func(t *testing.T) {
				w := newWorld(t)
				servers := map[string]string{"a": deadServer(t), "b": deadServer(t)}
				writeConfig(t, fmt.Sprintf("default_profile = \"a\"\ntoken_store = \"file\"\n\n[profiles.a]\nserver = %q\n\n[profiles.b]\nserver = %q\n", servers["a"], servers["b"]))
				t.Setenv("LOOMUX_PROFILE", tc.env)
				for p, server := range servers {
					if err := w.store.Set(auth.Account{Profile: p, Server: server}, "token-"+p); err != nil {
						t.Fatal(err)
					}
				}
				var stdout, stderr output
				if code := run(tc.args, &stdout, &stderr); code != 1 {
					t.Fatalf("exit %d, want 1 (the servers aren't there); stderr %q", code, stderr.String())
				}
				other := map[string]string{"a": "b", "b": "a"}[tc.want]
				if got := stderr.String(); !strings.Contains(got, servers[tc.want]) || strings.Contains(got, servers[other]) {
					t.Errorf("stderr = %q, want profile %s's server %s", got, tc.want, servers[tc.want])
				}
			})
		}
	}
}

func TestLoginAndLogoutUsage(t *testing.T) {
	newWorld(t)
	for _, args := range [][]string{
		{"login", "extra"}, {"login", "--nope"}, {"login", "--profile"}, {"login", "-h"},
		{"logout", "extra"}, {"logout", "--no-browser"}, {"logout", "--profile"},
		{"--profile"}, {"--profile", "test"}, {"--profile", "test", "attach"}, {"--nope", "login"},
	} {
		var stdout, stderr output
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if stdout.String() != "" || !strings.HasPrefix(stderr.String(), "usage: loomux") {
			t.Errorf("%q: stdout %q, stderr %q: want the usage", args, stdout.String(), stderr.String())
		}
	}
}

func TestLoginAndLogoutConfigError(t *testing.T) {
	for _, cmd := range []string{"login", "logout"} {
		newWorld(t)
		writeConfig(t, "token = \"abc\"\n")
		var stdout, stderr output
		if code := run([]string{cmd}, &stdout, &stderr); code != 1 {
			t.Errorf("%s: exit %d, want 1", cmd, code)
		}
		if got := stderr.String(); !strings.HasPrefix(got, "loomux: "+cmd+": ") || !strings.Contains(got, "never holds a token") {
			t.Errorf("%s: stderr = %q, want the config's error", cmd, got)
		}
	}
}

func TestLogoutSignsOut(t *testing.T) {
	w := newWorld(t)
	stored := w.srv.SeedLinkedSession("laptop", registry.SessionKindTUI)
	if err := w.store.Set(w.account, stored); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr output
	if code := run([]string{"logout"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "Signed out") || !strings.Contains(out, `"test"`) || !strings.Contains(out, w.srv.URL) {
		t.Errorf("stdout = %q", out)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
	w.wantNothingStored(t)
	c, err := client.New(w.srv.URL, stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions(context.Background()); !client.Unauthorized(err) {
		t.Errorf("the server still takes the token: %v", err)
	}
}

func TestLogoutNotSignedIn(t *testing.T) {
	newWorld(t)
	var stdout, stderr output
	if code := run([]string{"logout"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "Not signed in") || !strings.Contains(out, `"test"`) {
		t.Errorf("stdout = %q", out)
	}
}

// The server can't be reached: the token is deleted all the same, and
// the user is told the session may still be there.
func TestLogoutWhenTheServerCantBeReached(t *testing.T) {
	w := newWorld(t)
	dead := deadServer(t)
	writeConfig(t, fmt.Sprintf("token_store = \"file\"\n\n[profiles.test]\nserver = %q\n", dead))
	w.account.Server = dead
	const stored = "q8yG0f2bS1v4mJ0xZt7KcW9hN3eLr5aPdU6iO2sYxQk"
	if err := w.store.Set(w.account, stored); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr output
	if code := run([]string{"logout"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1: the session wasn't revoked", code)
	}
	got := stderr.String()
	for _, want := range []string{"loomux: logout: ", "couldn't be ended on the server", "token was deleted", "Settings → Devices"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr doesn't say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, stored) || strings.Contains(stdout.String(), stored) {
		t.Error("the token was printed")
	}
	if strings.Contains(stdout.String(), "Signed out") {
		t.Errorf("stdout = %q: it claims a clean sign-out", stdout.String())
	}
	w.wantNothingStored(t)
}

func TestWaitWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		auth.DefaultLinkTimeout: "10 minutes", // what the /link page says too
		time.Minute:             "1 minute",
		90 * time.Second:        "1m30s",
		30 * time.Second:        "30s",
		0:                       "10 minutes",
	} {
		if got := waitWords(d); got != want {
			t.Errorf("waitWords(%v) = %q, want %q", d, got, want)
		}
	}
}
