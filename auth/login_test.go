package auth

// The link flow against a real api.Server from the pinned server module
// (its api/apitest), as the design's "Testing" asks: 401 while the link
// waits, 200 once the browser has approved it, the same code on both
// sides, and no link from a linked session. The tests play the browser's
// half, the /link page of loomux/web: they read the link the terminal
// shows and POST /api/v1/sessions with a logged-in session's token.

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"
	"github.com/zalando/go-keyring"

	"github.com/Loomux/tui/client"
)

// ceiling bounds what a test waits for and expects to happen at once. It
// is long so that a machine that stalls for a while fails nothing.
const ceiling = 2 * time.Minute

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ceiling)
	t.Cleanup(cancel)
	return ctx
}

// seen is one request the terminal made.
type seen struct {
	method, uri, bearer string
	status              int
}

func (s seen) String() string { return fmt.Sprintf("%s %s → %d", s.method, s.uri, s.status) }

// linkServer is a real API server behind a front that records what the
// terminal sends. The browser's half goes to the API directly, so the
// record is the terminal's requests only.
type linkServer struct {
	api *apitest.Server
	// URL is the front's: the server the terminal is given.
	URL string
	// answer, when set, may answer the terminal's n-th request (from 1)
	// itself and report true, in place of the API.
	answer func(n int, w http.ResponseWriter, r *http.Request) bool

	mu   sync.Mutex
	reqs []seen
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func newLinkServer(t *testing.T) *linkServer {
	t.Helper()
	ls := &linkServer{api: apitest.New(t)}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		n := len(ls.reqs)
		ls.reqs = append(ls.reqs, seen{method: r.Method, uri: r.RequestURI,
			bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
		ls.mu.Unlock()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		if ls.answer == nil || !ls.answer(n+1, sw, r) {
			ls.api.API.ServeHTTP(sw, r)
		}
		ls.mu.Lock()
		ls.reqs[n].status = sw.status
		ls.mu.Unlock()
	}))
	t.Cleanup(front.Close)
	ls.URL = front.URL
	return ls
}

func (ls *linkServer) requests() []seen {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]seen(nil), ls.reqs...)
}

// waitRequests waits until the terminal has made n requests.
func (ls *linkServer) waitRequests(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(ceiling)
	for len(ls.requests()) < n {
		if time.Now().After(deadline) {
			t.Errorf("the terminal made %d requests, want %d", len(ls.requests()), n)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// linkHash reads the link as the /link page does: its fragment's h and
// label.
func linkHash(t *testing.T, link Link) (hash, label string) {
	t.Helper()
	i := strings.Index(link.URL, "#")
	if i < 0 {
		t.Fatalf("the link %q has no fragment", link.URL)
	}
	q, err := url.ParseQuery(link.URL[i+1:])
	if err != nil {
		t.Fatalf("the link %q: %v", link.URL, err)
	}
	return q.Get("h"), q.Get("label")
}

// approve is the /link page's Approve, by a session with token: POST
// /api/v1/sessions for the link's hash. It returns the status.
func (ls *linkServer) approve(t *testing.T, token string, link Link) int {
	t.Helper()
	hash, label := linkHash(t, link)
	body, _ := json.Marshal(map[string]string{"token_hash": hash, "label": label, "client_kind": "tui"})
	req, err := http.NewRequest(http.MethodPost, ls.api.URL+"/api/v1/sessions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("approve: %v", err)
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// sessions is what the sessions page lists for the session with token.
func (ls *linkServer) sessions(t *testing.T, token string) []client.Session {
	t.Helper()
	c, err := client.New(ls.api.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := c.Sessions(testCtx(t))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	return ss
}

func newFileStore(t *testing.T) *FileStore {
	t.Helper()
	return NewFileStore(filepath.Join(t.TempDir(), "loomux"))
}

func wantNothingStored(t *testing.T, s Store, a Account) {
	t.Helper()
	if _, err := s.Get(a); !errors.Is(err, ErrNotFound) {
		t.Errorf("the store's Get = %v, want ErrNotFound: nothing is stored", err)
	}
}

func TestLoginLinkFlow(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}

	var link Link
	shown := 0
	login := Login{Account: a, Store: store, Label: "my laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		shown++
		link = l
		wantNothingStored(t, store, a) // not before the approval
		go func() {
			ls.waitRequests(t, 4) // the link waits: a few polls first
			if status := ls.approve(t, browser, l); status != http.StatusCreated {
				t.Errorf("the browser's approval = %d, want 201", status)
			}
		}()
	}
	res, err := login.Run(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Already || res.Label != "my laptop" {
		t.Errorf("result = %+v, want a new sign-in as \"my laptop\"", res)
	}
	if shown != 1 {
		t.Fatalf("the link was shown %d times, want once", shown)
	}

	// The token is stored, and it's the one whose hash the link carried.
	token, err := store.Get(a)
	if err != nil {
		t.Fatalf("the token wasn't stored: %v", err)
	}
	hash, label := linkHash(t, link)
	if TokenHash(token) != hash {
		t.Error("the stored token isn't the one the link was for")
	}
	if label != "my laptop" || link.Label != "my laptop" {
		t.Errorf("the link's label = %q (%q in the fragment)", link.Label, label)
	}
	if link.URL != LinkURL(a.Server, hash, "my laptop") {
		t.Errorf("link = %q", link.URL)
	}
	// Both sides show the same code: the page computes it from the hash.
	if link.Code != LinkCode(hash) {
		t.Errorf("code = %q, want %q", link.Code, LinkCode(hash))
	}

	// The token went nowhere but the Authorization header.
	for what, s := range map[string]string{"the link": link.URL, "the code": link.Code,
		"the Login": fmt.Sprintf("%+v", login), "the result": fmt.Sprintf("%+v", res)} {
		if strings.Contains(s, token) {
			t.Errorf("%s holds the token", what)
		}
	}
	// 401 while the link waited, then 200: every request the poll, with
	// the pending token as its bearer and nothing in the URL.
	reqs := ls.requests()
	if len(reqs) < 5 {
		t.Fatalf("requests = %v, want the polls before the approval and the one after", reqs)
	}
	for i, r := range reqs {
		want := http.StatusUnauthorized
		if i == len(reqs)-1 {
			want = http.StatusOK
		}
		if r.method != http.MethodGet || r.uri != "/api/v1/sessions" || r.bearer != token || r.status != want {
			t.Errorf("request %d = %v (the pending token as bearer: %v), want GET /api/v1/sessions → %d", i+1, r, r.bearer == token, want)
		}
	}

	// On the server it is this terminal's session, as the page asked.
	var current []client.Session
	for _, s := range ls.sessions(t, token) {
		if s.Current {
			current = append(current, s)
		}
	}
	if len(current) != 1 || current[0].Label != "my laptop" || current[0].ClientKind != registry.SessionKindTUI {
		t.Errorf("the terminal's session = %+v, want one, labelled, of kind tui", current)
	}

	// The link can't be used twice: approving it again changes nothing.
	if status := ls.approve(t, browser, link); status != http.StatusConflict {
		t.Errorf("a second approval of the link = %d, want 409", status)
	}
	if n := len(ls.sessions(t, token)); n != 2 {
		t.Errorf("%d sessions after the second approval, want the browser's and the terminal's", n)
	}
}

// The label is cleaned whatever the caller gives: the page and the server
// refuse a long one or one with a control character.
func TestLoginCleansTheLabel(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	host := "build\x1b[2J-" + strings.Repeat("x", 80) + ".example.net"
	login := Login{Account: a, Store: store, Label: host, Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		if l.Label != LinkLabel(host) {
			t.Errorf("label = %q, want %q", l.Label, LinkLabel(host))
		}
		if status := ls.approve(t, browser, l); status != http.StatusCreated {
			t.Errorf("the browser's approval = %d, want 201: the server refused the label", status)
		}
	}
	res, err := login.Run(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Label != LinkLabel(host) {
		t.Errorf("signed in as %q, want %q", res.Label, LinkLabel(host))
	}
}

func TestLoginTimesOut(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	shown := 0
	_, err := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: 150 * time.Millisecond,
		Show: func(Link) { shown++ }}.Run(testCtx(t))
	if !errors.Is(err, ErrLinkTimeout) {
		t.Fatalf("Run = %v, want ErrLinkTimeout", err)
	}
	if shown != 1 {
		t.Errorf("the link was shown %d times", shown)
	}
	wantNothingStored(t, store, a)
}

func TestLoginCancelled(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	ctx, cancel := context.WithCancel(testCtx(t))
	_, err := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling,
		Show: func(Link) {
			go func() {
				ls.waitRequests(t, 3)
				cancel()
			}()
		}}.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	wantNothingStored(t, store, a)
}

// Cancelled before there is a link (ctrl+c while the server is asked):
// the error says so in a word, and a stored token stays.
func TestLoginCancelledBeforeTheLink(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		ls := newLinkServer(t)
		store := newFileStore(t)
		a := Account{Profile: "test", Server: ls.URL}
		if signedIn {
			if err := store.Set(a, ls.api.SeedLinkedSession("desk", registry.SessionKindTUI)); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(testCtx(t))
		cancel()
		_, err := Login{Account: a, Store: store, Label: "laptop",
			Show: func(Link) { t.Error("a link was shown") }}.Run(ctx)
		if !errors.Is(err, context.Canceled) || err.Error() != "cancelled" {
			t.Errorf("signed in %v: Run = %v, want \"cancelled\", a context.Canceled", signedIn, err)
		}
		if _, err := store.Get(a); signedIn != (err == nil) {
			t.Errorf("signed in %v: the store's Get = %v after the cancel", signedIn, err)
		}
	}
}

// An approval that lands just before the terminal stops waiting would
// leave a session nobody holds the token of: the terminal ends it.
func TestLoginUndoesAnApprovalItNeverSaw(t *testing.T) {
	for _, how := range []string{"cancelled", "timed out"} {
		t.Run(how, func(t *testing.T) {
			ls := newLinkServer(t)
			browser := ls.api.Login()
			store := newFileStore(t)
			a := Account{Profile: "test", Server: ls.URL}
			ctx, cancel := context.WithCancel(testCtx(t))
			// An interval that never comes: the approval is never polled.
			login := Login{Account: a, Store: store, Label: "laptop", Interval: time.Hour, Timeout: ceiling}
			want := context.Canceled
			if how == "timed out" {
				login.Timeout, want = 50*time.Millisecond, ErrLinkTimeout
			}
			login.Show = func(l Link) {
				if status := ls.approve(t, browser, l); status != http.StatusCreated {
					t.Errorf("the browser's approval = %d, want 201", status)
				}
				if how == "cancelled" {
					cancel()
				}
			}
			_, err := login.Run(ctx)
			if !errors.Is(err, want) {
				t.Fatalf("Run = %v, want %v", err, want)
			}
			wantNothingStored(t, store, a)
			if ss := ls.sessions(t, browser); len(ss) != 1 {
				t.Errorf("the server has %d sessions, want only the browser's: the approved one was left behind", len(ss))
			}
			if !strings.Contains(err.Error(), "undone") {
				t.Errorf("error %q doesn't say the approval was undone", err)
			}
		})
	}
}

// Signed in already: no link, and the stored token stays.
func TestLoginAlreadySignedIn(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	stored := ls.api.SeedLinkedSession("desk", registry.SessionKindTUI)
	if err := store.Set(a, stored); err != nil {
		t.Fatal(err)
	}
	res, err := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: time.Second,
		Show: func(Link) { t.Error("a link was shown to a profile that is signed in") }}.Run(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Already || res.Label != "desk" {
		t.Errorf("result = %+v, want already signed in as \"desk\"", res)
	}
	if got, err := store.Get(a); err != nil || got != stored {
		t.Errorf("the stored token changed (%v)", err)
	}
	if reqs := ls.requests(); len(reqs) != 1 || reqs[0].status != http.StatusOK {
		t.Errorf("requests = %v, want the one check of the stored token", reqs)
	}
}

// A stored token the server answers 401 to is dropped (the 401 rule), and
// the login goes on.
func TestLoginReplacesARejectedToken(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	if err := store.Set(a, token); err != nil { // never this server's
		t.Fatal(err)
	}
	login := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		wantNothingStored(t, store, a) // dropped at the 401, not at the approval
		if status := ls.approve(t, browser, l); status != http.StatusCreated {
			t.Errorf("the browser's approval = %d, want 201", status)
		}
	}
	res, err := login.Run(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Already {
		t.Errorf("result = %+v", res)
	}
	if got, err := store.Get(a); err != nil || got == token {
		t.Errorf("the rejected token wasn't replaced (%v)", err)
	}
}

// When the stored token can't be checked, nothing is concluded: it stays,
// and no link is shown.
func TestLoginStoredTokenCantBeChecked(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		server string
	}{
		"a server error": {answer: func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) }},
		"the auth proxy": {answer: func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://sso.example.net/login")
			w.WriteHeader(http.StatusFound)
		}},
		"an unreachable server": {server: down.URL},
	} {
		t.Run(name, func(t *testing.T) {
			ls := newLinkServer(t)
			ls.answer = func(_ int, w http.ResponseWriter, _ *http.Request) bool { tc.answer(w); return true }
			a := Account{Profile: "test", Server: ls.URL}
			if tc.server != "" {
				a.Server = tc.server
			}
			store := newFileStore(t)
			if err := store.Set(a, token); err != nil {
				t.Fatal(err)
			}
			_, err := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: time.Second,
				Show: func(Link) { t.Error("a link was shown") }}.Run(testCtx(t))
			if err == nil || errors.Is(err, ErrSignedOut) || errors.Is(err, ErrLinkTimeout) {
				t.Fatalf("Run = %v, want the check's error", err)
			}
			if strings.Contains(err.Error(), token) {
				t.Error("the error repeats the token")
			}
			if got, gerr := store.Get(a); gerr != nil || got != token {
				t.Errorf("the stored token is gone (%v) after %v", gerr, err)
			}
		})
	}
}

// No keyring: login refuses before anything is sent or shown, and says
// how to opt in to the file store (the design's "Storage and revocation").
func TestLoginRefusesWithoutAKeyring(t *testing.T) {
	keyring.MockInitWithError(errors.New("org.freedesktop.secrets was not provided by any .service files"))
	defer keyring.MockInit()
	ls := newLinkServer(t)
	a := Account{Profile: "test", Server: ls.URL}
	_, err := Login{Account: a, Store: NewKeyringStore("/x/config.toml"), Label: "laptop", Interval: 5 * time.Millisecond, Timeout: time.Second,
		Show: func(Link) { t.Error("a link was shown") }}.Run(testCtx(t))
	if !errors.Is(err, ErrNoKeyring) || !strings.Contains(err.Error(), `token_store = "file"`) {
		t.Errorf("Run = %v, want ErrNoKeyring with the way to the file store", err)
	}
	if reqs := ls.requests(); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", reqs)
	}
}

// A stored entry that isn't a token can't be signed in with: the login
// replaces it.
func TestLoginReplacesAnUnreadableEntry(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	if err := store.Set(a, token); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(a), []byte("not a token file"), 0o600); err != nil {
		t.Fatal(err)
	}
	login := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		if status := ls.approve(t, browser, l); status != http.StatusCreated {
			t.Errorf("the browser's approval = %d, want 201", status)
		}
	}
	if _, err := login.Run(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(a); err != nil || got == token {
		t.Errorf("the entry wasn't replaced by the new token (%v)", err)
	}
}

// Before it shows a link, the terminal checks that the server answers the
// new token as the API does a token it doesn't know: 401. Anything else
// would leave the user approving a link that can't work.
func TestLoginChecksTheServerFirst(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	jsonAnswer := func(status int, body string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}
	}
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		server string
		want   string
	}{
		"a server that takes any token": {answer: jsonAnswer(200, `{"sessions":[]}`), want: "accepted a token"},
		"the auth proxy's redirect": {answer: func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://sso.example.net/login?rd=x")
			w.WriteHeader(http.StatusFound)
		}, want: "auth proxy"},
		"the auth proxy's page": {answer: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>log in</html>")
		}, want: "auth proxy"},
		"not the API":           {answer: jsonAnswer(404, `{"error":"not found","code":"not_found"}`), want: "not found"},
		"a server error":        {answer: jsonAnswer(500, `{"error":"could not verify session","code":"internal"}`), want: "could not verify session"},
		"an unreachable server": {server: down.URL, want: "connect"},
	} {
		t.Run(name, func(t *testing.T) {
			ls := newLinkServer(t)
			ls.answer = func(_ int, w http.ResponseWriter, _ *http.Request) bool { tc.answer(w); return true }
			a := Account{Profile: "test", Server: ls.URL}
			if tc.server != "" {
				a.Server = tc.server
			}
			store := newFileStore(t)
			_, err := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: time.Second,
				Show: func(Link) { t.Error("a link was shown") }}.Run(testCtx(t))
			if err == nil || errors.Is(err, ErrLinkTimeout) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v, want an error saying %q", err, tc.want)
			}
			wantNothingStored(t, store, a)
			if tc.server == "" {
				if reqs := ls.requests(); len(reqs) != 1 {
					t.Errorf("requests = %v, want the one check", reqs)
				}
			}
		})
	}
}

// A poll that fails for a moment (the server restarting behind its proxy,
// the network away) is tried again: the wait goes on.
func TestLoginPollSurvivesTransientFailures(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	ls.answer = func(n int, w http.ResponseWriter, _ *http.Request) bool {
		switch n {
		case 2: // a reverse proxy's page for a server that's down
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "<html>502</html>")
		case 3:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"shutting down","code":"unavailable"}`)
		case 4:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"slow down","code":"rate_limited"}`)
		case 5: // the connection dropped
			conn, _, err := w.(*statusWriter).ResponseWriter.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		default:
			return false
		}
		return true
	}
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	login := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		if status := ls.approve(t, browser, l); status != http.StatusCreated {
			t.Errorf("the browser's approval = %d, want 201", status)
		}
	}
	if _, err := login.Run(testCtx(t)); err != nil {
		t.Fatalf("Run = %v, want the sign-in after the failures", err)
	}
	if _, err := store.Get(a); err != nil {
		t.Errorf("the token wasn't stored: %v", err)
	}
	if reqs := ls.requests(); len(reqs) != 6 {
		t.Errorf("requests = %v, want the check, four failed polls and the one that got in", reqs)
	}
}

// An answer that says the wait can't succeed ends it, and an approval
// given meanwhile is undone.
func TestLoginPollStopsOnAnAnswerThatCantChange(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter){
		"the auth proxy": func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://sso.example.net/login")
			w.WriteHeader(http.StatusFound)
		},
		"forbidden": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"no","code":"forbidden"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ls := newLinkServer(t)
			browser := ls.api.Login()
			ls.answer = func(n int, w http.ResponseWriter, _ *http.Request) bool {
				if n == 2 {
					answer(w)
				}
				return n == 2
			}
			store := newFileStore(t)
			a := Account{Profile: "test", Server: ls.URL}
			login := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
			login.Show = func(l Link) {
				if status := ls.approve(t, browser, l); status != http.StatusCreated {
					t.Errorf("the browser's approval = %d, want 201", status)
				}
			}
			_, err := login.Run(testCtx(t))
			if err == nil || errors.Is(err, ErrLinkTimeout) {
				t.Fatalf("Run = %v, want the answer's error", err)
			}
			wantNothingStored(t, store, a)
			if ss := ls.sessions(t, browser); len(ss) != 1 {
				t.Errorf("the server has %d sessions, want only the browser's", len(ss))
			}
		})
	}
}

// failingSet is a store that can't be written.
type failingSet struct {
	Store
	err error
}

func (f failingSet) Set(Account, string) error { return f.err }

// Approved, but the token can't be stored: the session would be one
// nobody can use or sign out of, so it is ended, and the error says so.
func TestLoginUndoesAnApprovalItCantStore(t *testing.T) {
	ls := newLinkServer(t)
	browser := ls.api.Login()
	a := Account{Profile: "test", Server: ls.URL}
	full := errors.New("no space left on device")
	store := failingSet{Store: newFileStore(t), err: full}
	login := Login{Account: a, Store: store, Label: "laptop", Interval: 5 * time.Millisecond, Timeout: ceiling}
	login.Show = func(l Link) {
		if status := ls.approve(t, browser, l); status != http.StatusCreated {
			t.Errorf("the browser's approval = %d, want 201", status)
		}
	}
	_, err := login.Run(testCtx(t))
	if !errors.Is(err, full) || !strings.Contains(err.Error(), "undone") {
		t.Fatalf("Run = %v, want the store's error and that the approval was undone", err)
	}
	wantNothingStored(t, store, a)
	if ss := ls.sessions(t, browser); len(ss) != 1 {
		t.Errorf("the server has %d sessions, want only the browser's", len(ss))
	}
}

// Only a session that logged in with the password can approve a link
// (S7): a terminal's own token can't mint another, so a stolen one can't
// outlive its revocation.
func TestLinkFromALinkedSessionIsRefused(t *testing.T) {
	ls := newLinkServer(t)
	linked := ls.api.SeedLinkedSession("laptop", registry.SessionKindTUI)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	login := Login{Account: a, Store: store, Label: "second", Interval: 5 * time.Millisecond, Timeout: 150 * time.Millisecond}
	login.Show = func(l Link) {
		if status := ls.approve(t, linked, l); status != http.StatusForbidden {
			t.Errorf("a linked session's approval = %d, want 403", status)
		}
	}
	if _, err := login.Run(testCtx(t)); !errors.Is(err, ErrLinkTimeout) {
		t.Fatalf("Run = %v, want ErrLinkTimeout: nobody approved", err)
	}
	wantNothingStored(t, store, a)
}
