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
	"github.com/Loomux/tui/ui/conversation"
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

func (s testStore) DeleteIf(a auth.Account, token string) (bool, error) {
	if stored, ok := s[a]; ok && stored == token {
		delete(s, a)
		return true, nil
	}
	return false, nil
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

// stored gives the screens a store holding token, and its account.
func stored(t *testing.T, token string) (testStore, auth.Account) {
	t.Helper()
	store := testStore{}
	a := auth.Account{Profile: "test"}
	if err := store.Set(a, token); err != nil {
		t.Fatal(err)
	}
	return store, a
}

// A 401 to the names request is the list's 401 too: polling stops, the
// token goes, and the screen says to sign in.
func TestListDropsTheTokenOnAWorkspaces401(t *testing.T) {
	api := openable()
	api.set(func() { api.wsErr = unauthorized })
	store, a := stored(t, testToken)
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	if !h.app.signedOut {
		t.Fatal("the names request's 401 didn't sign the list out")
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the names request's 401 left the token stored: %v", err)
	}
}

// A 401 to a take back's request signs out and drops the token.
func TestTakeBackDropsTheTokenOnTheAPIs401(t *testing.T) {
	api := openable()
	store, a := stored(t, testToken)
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	h.to("conv-held")
	api.set(func() { api.convErr = unauthorized })
	h.key("T")
	h.flush()
	if !h.app.signedOut {
		t.Error("the take back's 401 didn't sign the list out")
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the take back's 401 left the token stored: %v", err)
	}
}

// attach401 is the list's attach endpoints: the first request is the
// API's own 401.
type attach401 struct{ *fakeAPI }

func (attach401) AttachInfo(context.Context, string) (*client.AttachInfo, error) {
	return nil, unauthorized
}

func (attach401) Takeover(context.Context, string, client.TakeoverRequest) (*client.Takeover, error) {
	return nil, unauthorized
}

func (attach401) Heartbeat(context.Context, string, string) (*client.Hold, error) {
	return nil, unauthorized
}

// A 401 to an attach's request signs out and drops the token.
func TestAttachDropsTheTokenOnTheAPIs401(t *testing.T) {
	api := openable()
	api.convs["conv-attn"].Tasks = []client.Task{{ID: "t-attn", Kind: "agent", Status: client.TaskRunning}}
	store, a := stored(t, testToken)
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken, Attach: &Attach{API: attach401{api}}})
	h.to("conv-attn")
	h.key("a")
	h.flush()
	if !h.app.signedOut {
		t.Error("the attach's 401 didn't sign the list out")
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the attach's 401 left the token stored: %v", err)
	}
}

// countingStore counts the 401 rule's delete attempts, and can fail
// them.
type countingStore struct {
	testStore
	deletes int
	err     error
}

func (s *countingStore) DeleteIf(a auth.Account, token string) (bool, error) {
	s.deletes++
	if s.err != nil {
		return false, s.err
	}
	return s.testStore.DeleteIf(a, token)
}

// A late conversation 401 after the list signed out doesn't delete
// again or replace the first answer.
func TestLate401KeepsTheFirstSignedOutAnswer(t *testing.T) {
	api := openable()
	store := &countingStore{testStore: testStore{}, err: errors.New("device busy")}
	a := auth.Account{Profile: "test"}
	if err := store.Set(a, testToken); err != nil {
		t.Fatal(err)
	}
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	api.set(func() { api.err = unauthorized })
	h.send(refreshMsg{})
	h.flush()
	if store.deletes != 1 || h.app.pollErr == nil || !strings.Contains(h.app.pollErr.Error(), "device busy") {
		t.Fatalf("after the list's 401: %d deletes, answer %v; want one delete that says why", store.deletes, h.app.pollErr)
	}
	first := h.app.pollErr
	h.send(conversation.SignedOutMsg{Err: unauthorized})
	h.flush()
	if store.deletes != 1 {
		t.Errorf("%d delete attempts, want the sign-out to run once", store.deletes)
	}
	if h.app.pollErr != first {
		t.Errorf("a late 401 replaced the first answer: %v, want %v", h.app.pollErr, first)
	}
}

// A 401 that answers a request of the list's while it is in flight, and
// whose message the screens end before handling, is still the token's:
// Close waits for the request and keeps its 401. The poll, the
// workspaces, the attach's task lookup and a take back are all kept.
func TestCloseDropsTheTokenOnAnInFlight401(t *testing.T) {
	for _, c := range []struct {
		name string
		ask  func(h *harness, api *fakeAPI)
	}{
		{"poll", func(h *harness, api *fakeAPI) {
			api.set(func() { api.err = unauthorized })
			h.run(h.app.poll())
		}},
		{"workspaces", func(h *harness, api *fakeAPI) {
			api.set(func() { api.wsErr = unauthorized })
			h.run(h.app.fetchNames())
		}},
		{"task lookup", func(h *harness, api *fakeAPI) {
			api.set(func() { api.convErr = unauthorized })
			h.send(keyPress("a"))
		}},
		{"take back", func(h *harness, api *fakeAPI) {
			h.to("conv-held")
			api.set(func() { api.convErr = unauthorized })
			h.send(keyPress("T"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := openable()
			api.convs["conv-held"] = heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover,
				Hold: driveHold("t-held", "laptop")})["conv-held"]
			store, a := stored(t, testToken)
			h := handoff(t, api, Options{Store: store, Account: a, Token: testToken,
				Attach: &Attach{API: attach401{api}}})
			c.ask(h, api)
			if len(h.out) == 0 {
				t.Fatal("the request answered before its message could be left unhandled")
			}
			if err := h.app.Close(); !client.Unauthorized(err) {
				t.Errorf("Close = %v, want the pending request's 401", err)
			}
			if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("the pending request's 401 left the token stored: %v", err)
			}
		})
	}
}

// A newer login's token isn't the one a pending request's 401 refused: it
// survives Close.
func TestCloseKeepsANewerTokenOnAnInFlight401(t *testing.T) {
	api := openable()
	store := testStore{}
	a := auth.Account{Profile: "test"}
	newer := testToken + "-newer"
	if err := store.Set(a, newer); err != nil {
		t.Fatal(err)
	}
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	api.set(func() { api.err = unauthorized })
	h.run(h.app.poll())
	if len(h.out) == 0 {
		t.Fatal("the poll answered before its message could be left unhandled")
	}
	if err := h.app.Close(); !client.Unauthorized(err) {
		t.Errorf("Close = %v, want the pending poll's 401", err)
	}
	if got, err := store.Get(a); err != nil || got != newer {
		t.Errorf("stored token = %q (%v), want the newer one", got, err)
	}
}

// A transcript's fetch can answer after its transcript was left: the 401
// is still the token's, and the root applies it.
func TestLeftTranscriptDropsTheTokenOnIts401(t *testing.T) {
	api := openable()
	api.convs["conv-await"] = &client.Conversation{ConversationID: "conv-await", Tasks: []client.Task{
		{ID: "t-await", Kind: "agent", Status: client.TaskRunning}}}
	store, a := stored(t, testToken)
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	h.to("conv-await")
	h.key("enter")
	api.set(func() { api.turnsErr = unauthorized })
	h.send(keyPress("p")) // the fetch answers 401, its message waits
	if len(h.out) == 0 {
		t.Fatal("the transcript answered before its message could be left unhandled")
	}
	h.send(keyPress("esc")) // back to the conversation: the transcript is left
	h.flush()               // the answer arrives for a transcript since left
	if !h.app.signedOut {
		t.Error("the left transcript's 401 didn't sign the list out")
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the left transcript's 401 left the token stored: %v", err)
	}
}

// A 401 that answers after its conversation was closed is the root's to
// apply: there is no screen left to tell.
func TestClosedConversationDropsTheTokenOnAPending401(t *testing.T) {
	api := openable()
	api.convs["conv-await"] = &client.Conversation{ConversationID: "conv-await", Tasks: []client.Task{
		{ID: "t-await", Kind: "agent", Status: client.TaskRunning}}}
	store, a := stored(t, testToken)
	h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
	h.to("conv-await")
	h.key("enter")
	api.set(func() { api.turnsErr = unauthorized })
	h.send(keyPress("p")) // the fetch answers 401, its message waits
	pending := h.out
	h.out = nil
	h.key("esc", "esc") // the transcript and the conversation are closed
	if h.app.conv != nil {
		t.Fatal("the conversation is still open")
	}
	h.out = pending // the answer arrives with nothing open to take it
	h.flush()
	if !h.app.signedOut {
		t.Error("the closed conversation's pending 401 didn't sign the list out")
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the closed conversation's pending 401 left the token stored: %v", err)
	}
}

// holdingPoll answers the list's poll only when the test lets it.
type holdingPoll struct {
	*fakeAPI
	entered chan struct{}
	answer  chan struct{}
}

func (h *holdingPoll) Conversations(context.Context, int) (*client.ConversationPage, error) {
	close(h.entered)
	<-h.answer
	return nil, unauthorized
}

// A poll 401 still on its way when the screens stop is waited for: Close
// keeps it and drops the token.
func TestCloseWaitsForAnInFlightPoll401(t *testing.T) {
	api := &holdingPoll{fakeAPI: openable(), entered: make(chan struct{}), answer: make(chan struct{})}
	store, a := stored(t, testToken)
	app := New(Options{Profile: "test", API: api, Store: store, Account: a, Token: testToken})
	app.tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	go func() { _ = app.poll()() }()
	<-api.entered
	closed := make(chan error, 1)
	go func() { closed <- app.Close() }()
	close(api.answer)
	if err := <-closed; !client.Unauthorized(err) {
		t.Errorf("Close = %v, want the pending poll's 401", err)
	}
	if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("the pending poll's 401 left the token stored: %v", err)
	}
}

// A program can stop before Bubble Tea runs a command it was handed: the
// request it counts never answers, so Close gives up on it after
// closeWait instead of hanging.
func TestCloseGivesUpOnACommandThatNeverRuns(t *testing.T) {
	api := openable()
	store, a := stored(t, testToken)
	app := New(Options{Profile: "test", API: api, Store: store, Account: a, Token: testToken})
	app.closeWait = 50 * time.Millisecond
	// The poll and the names are asked for and the program stops: their
	// commands are never run.
	_ = app.poll()
	_ = app.fetchNames()
	start := time.Now()
	if err := app.Close(); err != nil {
		t.Errorf("Close = %v, want nothing to report", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v with commands the program never ran; want it to give up", d)
	}
}

// A conversation request's 401 whose message the screens end before
// handling is still the token's: the conversation's requests are counted
// and kept with the list's.
func TestCloseDropsTheTokenOnAnUnhandledConversation401(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h *harness, api *fakeAPI)
	}{
		{"conversation", func(h *harness, api *fakeAPI) {
			h.to("conv-await")
			api.set(func() { api.convErr = unauthorized })
			h.send(keyPress("enter")) // the fetch's 401 waits, unhandled
		}},
		{"transcript", func(h *harness, api *fakeAPI) {
			api.convs["conv-await"] = &client.Conversation{ConversationID: "conv-await", Tasks: []client.Task{
				{ID: "t-await", Kind: "agent", Status: client.TaskRunning}}}
			h.to("conv-await")
			h.key("enter")
			api.set(func() { api.turnsErr = unauthorized })
			h.send(keyPress("p")) // the transcript's 401 waits, unhandled
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := openable()
			store, a := stored(t, testToken)
			h := handoff(t, api, Options{Store: store, Account: a, Token: testToken})
			c.setup(h, api)
			if len(h.out) == 0 {
				t.Fatal("the request answered before its message could be left unhandled")
			}
			if err := h.app.Close(); !client.Unauthorized(err) {
				t.Errorf("Close = %v, want the pending request's 401", err)
			}
			if _, err := store.Get(a); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("the pending request's 401 left the token stored: %v", err)
			}
		})
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
