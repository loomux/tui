package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Loomux/server/registry"
	"github.com/zalando/go-keyring"

	"github.com/Loomux/tui/client"
)

// failingDelete is a store whose token can't be deleted.
type failingDelete struct {
	Store
	err error
}

func (f failingDelete) Delete(Account) error { return f.err }

func (f failingDelete) DeleteIf(Account, string) (bool, error) { return false, f.err }

// Logout ends the session on the server first, then deletes the token.
func TestLogoutRevokesThenDeletes(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	stored := ls.api.SeedLinkedSession("laptop", registry.SessionKindTUI)
	if err := store.Set(a, stored); err != nil {
		t.Fatal(err)
	}
	ls.answer = func(int, http.ResponseWriter, *http.Request) bool {
		if got, err := store.Get(a); err != nil || got != stored {
			t.Errorf("the token was deleted before the server was asked (%v)", err)
		}
		return false
	}
	res, err := Logout(testCtx(t), store, a)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stored || res.NotRevoked != nil {
		t.Errorf("result = %+v, want a stored token, revoked", res)
	}
	wantNothingStored(t, store, a)
	reqs := ls.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].uri != "/api/v1/logout" || reqs[0].bearer != stored || reqs[0].status != http.StatusNoContent {
		t.Errorf("requests = %v, want POST /api/v1/logout → 204 with the stored token", reqs)
	}
	c, err := client.New(ls.api.URL, stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions(testCtx(t)); !client.Unauthorized(err) {
		t.Errorf("the server still takes the token after logout: %v", err)
	}
}

func TestLogoutNotSignedIn(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	res, err := Logout(testCtx(t), store, a)
	if err != nil || res.Stored || res.NotRevoked != nil {
		t.Errorf("Logout = %+v, %v; want nothing to do", res, err)
	}
	if reqs := ls.requests(); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", reqs)
	}
}

// The server can't be asked: the token is deleted all the same, and the
// result says the session may still be there.
func TestLogoutDeletesWhenTheServerCantBeAsked(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		server string
	}{
		"an unreachable server": {server: down.URL},
		"a server error":        {answer: func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) }},
		"the auth proxy": {answer: func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://sso.example.net/login")
			w.WriteHeader(http.StatusFound)
		}},
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
			res, err := Logout(testCtx(t), store, a)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Stored || res.NotRevoked == nil {
				t.Fatalf("result = %+v, want a stored token that wasn't revoked", res)
			}
			if strings.Contains(res.NotRevoked.Error(), token) {
				t.Error("the error repeats the token")
			}
			wantNothingStored(t, store, a)
		})
	}
}

// A 401 to the logout is a session that has ended already: that is
// signed out, with nothing left to revoke.
func TestLogoutOfASessionThatHasEnded(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	if err := store.Set(a, token); err != nil { // never this server's
		t.Fatal(err)
	}
	res, err := Logout(testCtx(t), store, a)
	if err != nil || !res.Stored || res.NotRevoked != nil {
		t.Errorf("Logout = %+v, %v; want signed out with nothing left on the server", res, err)
	}
	wantNothingStored(t, store, a)
}

// An entry that isn't a token can't be sent to the server: it is cleared,
// and the result says no session was revoked.
func TestLogoutClearsAnUnreadableEntry(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	if err := store.Set(a, token); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(a), []byte("not a token file"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Logout(testCtx(t), store, a)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stored || !errors.Is(res.NotRevoked, ErrInvalid) {
		t.Errorf("result = %+v, want an entry cleared without a revocation, as ErrInvalid", res)
	}
	if _, err := os.Stat(store.path(a)); !os.IsNotExist(err) {
		t.Errorf("the entry is still there: %v", err)
	}
	if reqs := ls.requests(); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", reqs)
	}
}

// A token that can't be read is cleared too: logout leaves none behind.
func TestLogoutClearsATokenItCantRead(t *testing.T) {
	ls := newLinkServer(t)
	a := Account{Profile: "test", Server: ls.URL}
	files := newFileStore(t)
	if err := files.Set(a, token); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("token file: permission denied")
	res, err := Logout(testCtx(t), failingGet{Store: files, err: denied}, a)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stored || !errors.Is(res.NotRevoked, denied) {
		t.Errorf("result = %+v, want the read's error as why nothing was revoked", res)
	}
	wantNothingStored(t, files, a)
	if reqs := ls.requests(); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", reqs)
	}
}

func TestLogoutWithoutAKeyring(t *testing.T) {
	keyring.MockInitWithError(errors.New("org.freedesktop.secrets was not provided by any .service files"))
	defer keyring.MockInit()
	ls := newLinkServer(t)
	a := Account{Profile: "test", Server: ls.URL}
	if _, err := Logout(testCtx(t), NewKeyringStore("/x/config.toml"), a); !errors.Is(err, ErrNoKeyring) {
		t.Errorf("Logout = %v, want ErrNoKeyring", err)
	}
	if reqs := ls.requests(); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", reqs)
	}
}

// The token can't be deleted: that is the error, and the result still
// says the server ended the session.
func TestLogoutThatCantDelete(t *testing.T) {
	ls := newLinkServer(t)
	a := Account{Profile: "test", Server: ls.URL}
	stored := ls.api.SeedLinkedSession("laptop", registry.SessionKindTUI)
	files := newFileStore(t)
	if err := files.Set(a, stored); err != nil {
		t.Fatal(err)
	}
	busy := errors.New("device busy")
	res, err := Logout(testCtx(t), failingDelete{Store: files, err: busy}, a)
	if !errors.Is(err, busy) {
		t.Fatalf("Logout = %v, want the store's error", err)
	}
	if !res.Stored || res.NotRevoked != nil {
		t.Errorf("result = %+v, want revoked on the server", res)
	}
}

// The 401 rule (the design's "Storage and revocation"): the API's 401 to
// a request made with the stored token drops that token, and the answer
// is "signed out: run `loomux login`".
func TestSignedOutDropsTheStoredToken(t *testing.T) {
	ls := newLinkServer(t)
	store := newFileStore(t)
	a := Account{Profile: "test", Server: ls.URL}
	stored := ls.api.SeedLinkedSession("laptop", registry.SessionKindTUI)
	if err := store.Set(a, stored); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(a.Server, stored)
	if err != nil {
		t.Fatal(err)
	}
	// While the server takes the token, nothing happens.
	_, err = c.Sessions(testCtx(t))
	if got := SignedOut(store, a, stored, err); got != nil {
		t.Fatalf("SignedOut on a good answer = %v", got)
	}
	if got, err := store.Get(a); err != nil || got != stored {
		t.Fatalf("the token of a session that is signed in was dropped (%v)", err)
	}
	// The session is revoked from the web's sessions page.
	if err := c.Logout(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	_, err = c.Sessions(testCtx(t))
	got := SignedOut(store, a, stored, err)
	if !errors.Is(got, ErrSignedOut) {
		t.Fatalf("SignedOut on the API's 401 = %v, want ErrSignedOut", got)
	}
	if !strings.Contains(got.Error(), "signed out") || !strings.Contains(got.Error(), "loomux login") {
		t.Errorf("ErrSignedOut says %q, want signed out and the login command", got)
	}
	wantNothingStored(t, store, a)
}

// Only the API's own 401 signs out. No other failure says the token is
// bad, so none of them costs the user a login.
func TestSignedOutLeavesOtherErrorsAlone(t *testing.T) {
	store := newFileStore(t)
	if err := store.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"no error":                      nil,
		"a network error":               errors.New("dial tcp: connection refused"),
		"a cancelled request":           context.Canceled,
		"a server error":                &client.Error{Status: http.StatusInternalServerError, Code: client.CodeInternal},
		"forbidden":                     &client.Error{Status: http.StatusForbidden, Code: client.CodeForbidden},
		"the auth proxy's page":         &client.InterceptedError{Status: http.StatusOK, ContentType: "text/html"},
		"the auth proxy's own 401 page": &client.InterceptedError{Status: http.StatusUnauthorized, ContentType: "text/html"},
	} {
		if got := SignedOut(store, acct, token, err); got != err {
			t.Errorf("%s: SignedOut = %v, want the error as it was", name, got)
		}
		if got, gerr := store.Get(acct); gerr != nil || got != token {
			t.Errorf("%s: the stored token was dropped (%v)", name, gerr)
		}
	}
}

// A 401 to an older token doesn't drop a newer one: `loomux login` ran in
// another terminal since this request's token was read.
func TestSignedOutKeepsANewerToken(t *testing.T) {
	store := newFileStore(t)
	newer := token + "2"
	if err := store.Set(acct, newer); err != nil {
		t.Fatal(err)
	}
	got := SignedOut(store, acct, token, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized})
	if !errors.Is(got, ErrSignedOut) {
		t.Errorf("SignedOut = %v, want ErrSignedOut: this run's token is refused", got)
	}
	if stored, err := store.Get(acct); err != nil || stored != newer {
		t.Errorf("the newer token was dropped (%v)", err)
	}
}

func TestSignedOutWithNothingStored(t *testing.T) {
	store := newFileStore(t)
	got := SignedOut(store, acct, token, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized})
	if got != ErrSignedOut {
		t.Errorf("SignedOut = %v, want ErrSignedOut itself", got)
	}
}

func TestSignedOutSaysWhenTheTokenCantBeDeleted(t *testing.T) {
	files := newFileStore(t)
	if err := files.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	busy := errors.New("device busy")
	got := SignedOut(failingDelete{Store: files, err: busy}, acct, token, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized})
	if !errors.Is(got, ErrSignedOut) || !strings.Contains(got.Error(), "device busy") {
		t.Errorf("SignedOut = %v, want ErrSignedOut naming why the token is still stored", got)
	}
	if strings.Contains(got.Error(), token) {
		t.Error("the error repeats the token")
	}
}
