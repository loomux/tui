package ui

// The 401 rule at the screens, against a real api.Server from the pinned
// server module (api/apitest): the API's own 401 to a request made with
// the stored token deletes that token, a newer login's survives, and a
// 401 that isn't the API's (a proxy's page) deletes nothing.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
)

// testToken is what the harness's token store holds when its list signs
// out.
const testToken = "test-token"

// testStore is a memory auth.Store.
type testStore map[auth.Account]string

func (s testStore) Get(a auth.Account) (string, error) {
	token, ok := s[a]
	if !ok {
		return "", auth.ErrNotFound
	}
	return token, nil
}

func (s testStore) Set(a auth.Account, token string) error {
	s[a] = token
	return nil
}

func (s testStore) Delete(a auth.Account) error {
	delete(s, a)
	return nil
}

// withTestStore gives o a store holding token, the token this run's API
// was made with.
func withTestStore(o Options, token string) Options {
	store := testStore{}
	a := auth.Account{Profile: "test"}
	_ = store.Set(a, token)
	o.Store, o.Account, o.Token = store, a, token
	return o
}

// refusedSession is a server whose session the client ends: the token
// the list's API was made with is then the API's own 401.
func refusedSession(t *testing.T) (*apitest.Server, *client.Client, string) {
	t.Helper()
	srv := apitest.New(t)
	token := srv.SeedLinkedSession("laptop", registry.SessionKindTUI)
	c, err := client.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	return srv, c, token
}

// signedOutApp starts the list on c: its first poll is the 401.
func signedOutApp(t *testing.T, server string, c *client.Client, store auth.Store, a auth.Account, token string) *harness {
	t.Helper()
	h := &harness{t: t, now: time.Now()}
	h.app = New(Options{Profile: "test", API: c, Server: server, Store: store, Account: a, Token: token})
	h.app.tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	h.run(h.app.Init())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 24})
	h.flush()
	return h
}

// The API's own 401 drops the stored token, and the screen says to sign
// in with the login command.
func TestListDropsTheStoredTokenOnTheAPIs401(t *testing.T) {
	srv, c, token := refusedSession(t)
	store := testStore{}
	a := auth.Account{Profile: "test", Server: srv.URL}
	if err := store.Set(a, token); err != nil {
		t.Fatal(err)
	}
	h := signedOutApp(t, srv.URL, c, store, a, token)
	if f := h.frame(); !strings.Contains(f, "⚠ signed out") || !strings.Contains(f, "signed out: run") || !strings.Contains(f, "loomux login") {
		t.Errorf("the list after the API's 401:\n%s", f)
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the refused token is still stored: %v", err)
	}
}

// A 401 to this run's token doesn't drop a newer one: `loomux login` ran
// in another terminal since the token was read.
func TestListKeepsANewerToken(t *testing.T) {
	srv, c, token := refusedSession(t)
	store := testStore{}
	a := auth.Account{Profile: "test", Server: srv.URL}
	newer := token + "-newer"
	if err := store.Set(a, newer); err != nil {
		t.Fatal(err)
	}
	h := signedOutApp(t, srv.URL, c, store, a, token)
	if f := h.frame(); !strings.Contains(f, "loomux login") {
		t.Errorf("the list after the API's 401:\n%s", f)
	}
	if got, err := store.Get(a); err != nil || got != newer {
		t.Errorf("stored token = %q (%v), want the newer one", got, err)
	}
}

// A 401 that isn't the API's own, such as an auth proxy's page, says
// nothing about the token: it stays.
func TestListKeepsTheTokenOnAProxy401(t *testing.T) {
	front := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(rw, "<html><body>sign in</body></html>")
	}))
	t.Cleanup(front.Close)
	token := "a-token"
	c, err := client.New(front.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	store := testStore{}
	a := auth.Account{Profile: "test", Server: front.URL}
	if err := store.Set(a, token); err != nil {
		t.Fatal(err)
	}
	h := signedOutApp(t, front.URL, c, store, a, token)
	if f := h.frame(); !strings.Contains(f, "auth proxy") {
		t.Errorf("the list after the proxy's 401:\n%s", f)
	}
	if h.app.signedOut {
		t.Error("the proxy's 401 signed the list out")
	}
	if got, err := store.Get(a); err != nil || got != token {
		t.Errorf("stored token = %q (%v), want it kept", got, err)
	}
}
