package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Loomux/tui/client"
)

// The link flow's wait: the design's numbers, which the /link page
// repeats to the user ("It stops after 10 minutes").
const (
	DefaultLinkInterval = 2 * time.Second
	DefaultLinkTimeout  = 10 * time.Minute
)

// callTimeout bounds one request of the flow, so a connection that hangs
// costs one poll, not the wait.
const callTimeout = 15 * time.Second

// ErrLinkTimeout: nobody approved the link while the terminal waited.
// Nothing was stored.
var ErrLinkTimeout = errors.New("timed out waiting for the approval")

// Link is what `loomux login` shows: the link to open in a browser that
// is signed in to Loomux, and the code its page must show. Neither is a
// secret. The token itself is never in a Link.
type Link struct {
	// URL is <server>/link#h=<token hash>&label=<label>.
	URL string
	// Code is the verification code, ABCD-EFGH.
	Code string
	// Label is the session's label, as the page will show it.
	Label string
}

// Login signs an account in through the link flow (the design's "The
// link flow"): the terminal makes its own token, the browser approves
// its hash, and the token is stored once the server takes it.
type Login struct {
	Account Account
	Store   Store
	// Label names this terminal's session, usually the host name. It is
	// cleaned by LinkLabel.
	Label string
	// Show is called once the server has answered and the wait begins,
	// with the link and the code for the user.
	Show func(Link)
	// Interval and Timeout are how often the server is asked and for
	// how long: DefaultLinkInterval and DefaultLinkTimeout when zero.
	Interval time.Duration
	Timeout  time.Duration
}

// LoginResult is a profile that is signed in.
type LoginResult struct {
	// Label is the session's label on the server.
	Label string
	// Already says the stored token was still good: no link was shown
	// and nothing changed.
	Already bool
}

// Run signs the account in. It returns when the link is approved and the
// token stored, or with an error and nothing stored: ErrLinkTimeout when
// nobody approved in time, ctx's error when it was cancelled, the
// store's when it can't be used (ErrNoKeyring, ErrInsecure), or the
// server's.
//
// The token is made here and kept here: it is sent only as the bearer
// token of the requests to the account's server, and stored only after
// the server has accepted it. It is never in the link, an error or the
// result.
func (l Login) Run(ctx context.Context) (LoginResult, error) {
	if err := l.Account.check(); err != nil {
		return LoginResult{}, err
	}
	label := LinkLabel(l.Label)

	// The store first: one that can't be used refuses the login before
	// anything is shown, and a token the server still takes is signed
	// in already.
	switch stored, err := l.Store.Get(l.Account); {
	case err == nil:
		c, err := client.New(l.Account.Server, stored)
		if err != nil {
			return LoginResult{}, err
		}
		ss, err := sessions(ctx, c)
		if err == nil {
			cur, _ := current(ss)
			return LoginResult{Label: cur.Label, Already: true}, nil
		}
		if ctx.Err() != nil {
			return LoginResult{}, cancelledError{ctx.Err()}
		}
		// The 401 rule: a token the server refuses is dropped, and the
		// login goes on. Any other failure says nothing about it.
		if err := SignedOut(l.Store, l.Account, stored, err); !errors.Is(err, ErrSignedOut) {
			return LoginResult{}, fmt.Errorf("checking the stored token: %w", err)
		}
	case errors.Is(err, ErrNoKeyring), errors.Is(err, ErrInsecure):
		return LoginResult{}, err
	}
	// ErrNotFound, or an entry that isn't a token: the new one replaces
	// it.

	token := NewToken()
	hash := TokenHash(token)
	c, err := client.New(l.Account.Server, token)
	if err != nil {
		return LoginResult{}, err
	}

	// Before a link is shown, the server must answer the new token as
	// the API answers one it doesn't know. An auth proxy that stops the
	// request, a wrong URL or a server that is down would leave the user
	// approving a link that can't work.
	switch _, err := sessions(ctx, c); {
	case err == nil:
		return LoginResult{}, fmt.Errorf("%s accepted a token it has never been given, so it isn't checking tokens: is the server URL right? Nothing was stored", l.Account.Server)
	case ctx.Err() != nil:
		return LoginResult{}, cancelledError{ctx.Err()}
	case !client.Unauthorized(err):
		return LoginResult{}, fmt.Errorf("can't start the login at %s: %w", l.Account.Server, err)
	}

	if l.Show != nil {
		l.Show(Link{URL: LinkURL(l.Account.Server, hash, label), Code: LinkCode(hash), Label: label})
	}
	ss, err := l.wait(ctx, c)
	if err != nil {
		return LoginResult{}, giveUp(c, label, err)
	}
	if err := l.Store.Set(l.Account, token); err != nil {
		return LoginResult{}, giveUp(c, label, fmt.Errorf("the link was approved, but the token couldn't be stored: %w", err))
	}
	if cur, ok := current(ss); ok && cur.Label != "" {
		label = cur.Label
	}
	return LoginResult{Label: label}, nil
}

// wait asks the server with the pending token until it takes it: GET
// /sessions answers 401 while the link isn't approved, then 200 with the
// sessions.
func (l Login) wait(parent context.Context, c *client.Client) ([]client.Session, error) {
	interval, timeout := l.Interval, l.Timeout
	if interval <= 0 {
		interval = DefaultLinkInterval
	}
	if timeout <= 0 {
		timeout = DefaultLinkTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()

	// failed is the last poll's error, when it failed rather than got
	// the 401.
	var failed error
	ended := func() error {
		if parent.Err() != nil {
			return cancelledError{parent.Err()}
		}
		if failed != nil {
			return fmt.Errorf("%w (the last check failed: %v)", ErrLinkTimeout, failed)
		}
		return ErrLinkTimeout
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ended()
		case <-tick.C:
		}
		ss, err := sessions(ctx, c)
		switch {
		case err == nil:
			return ss, nil
		case ctx.Err() != nil:
			return nil, ended()
		case client.Unauthorized(err):
			failed = nil // not approved yet
		case transient(err):
			failed = err
		default:
			return nil, err
		}
	}
}

// transient reports whether a poll's failure may pass: the request didn't
// get through, or the server (or a proxy before it) couldn't answer it
// now. An answer that says the request is wrong, or a proxy that stopped
// it, won't change by asking again.
func transient(err error) bool {
	var e *client.Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests
	}
	var stopped *client.InterceptedError
	return !errors.As(err, &stopped)
}

// giveUp ends a login that stores nothing, for cause. The link may have
// been approved all the same (just before the wait ended, or before the
// store failed), which would leave a session whose token nobody holds:
// the server is asked to end the pending token's session, and the error
// says what became of it.
func giveUp(c *client.Client, label string, cause error) error {
	ended, err := revoke(context.Background(), c)
	switch {
	case ended:
		return fmt.Errorf("%w (the approval was undone: this terminal isn't signed in)", cause)
	case err != nil:
		return fmt.Errorf("%w (the server couldn't be asked to undo an approval: if the link was approved, revoke the session %q in the web's Settings → Devices)", cause, label)
	}
	return cause // never approved
}

// sessions is GET /sessions, within callTimeout.
func sessions(ctx context.Context, c *client.Client) ([]client.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return c.Sessions(ctx)
}

// current is the session the request was made with.
func current(ss []client.Session) (client.Session, bool) {
	for _, s := range ss {
		if s.Current {
			return s, true
		}
	}
	return client.Session{}, false
}

// cancelledError is a context's end in words for a person. It unwraps to
// the context's error.
type cancelledError struct{ cause error }

func (e cancelledError) Error() string { return "cancelled" }
func (e cancelledError) Unwrap() error { return e.cause }
