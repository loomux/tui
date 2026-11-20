package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Loomux/tui/client"
)

// ErrSignedOut: the server no longer takes the profile's token (revoked
// from the web, expired, or logged out elsewhere), and the token has been
// dropped from the store.
var ErrSignedOut = errors.New("signed out: run `loomux login`")

// SignedOut applies the 401 rule (the design's "Storage and revocation")
// to err, the outcome of a request made with token, the account's stored
// token. Every such request's error goes through it.
//
// The API's 401 (client.Unauthorized) means the token is dead: it is
// deleted from the store and the result is ErrSignedOut, which also says
// so if the token couldn't be deleted. Any other err, nil included, is
// returned as it is: a network failure, a server error or an auth
// proxy's page says nothing about the token.
//
// The link flow's poll never comes here. Its token is the pending one,
// which isn't stored yet, and its 401 means "not approved yet".
func SignedOut(s Store, a Account, token string, err error) error {
	if !client.Unauthorized(err) {
		return err
	}
	if derr := drop(s, a, token); derr != nil {
		return fmt.Errorf("%w (and the token couldn't be deleted from the token store: %v)", ErrSignedOut, derr)
	}
	return ErrSignedOut
}

// drop deletes the account's stored token if it is still token. A
// different one is a newer login's, made since token was read (`loomux
// login` in another terminal): it isn't the one the server refused, and
// it stays.
func drop(s Store, a Account, token string) error {
	stored, err := s.Get(a)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	case stored != token:
		return nil
	}
	return s.Delete(a)
}

// revokeTimeout bounds asking the server to end a session.
const revokeTimeout = 10 * time.Second

// revoke asks the server to end c's own session: POST /logout, which
// also ends its streams and holds. ended says the server ended it; a 401
// is a session that doesn't exist (any more), which is no error.
func revoke(ctx context.Context, c *client.Client) (ended bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	switch err := c.Logout(ctx); {
	case err == nil:
		return true, nil
	case client.Unauthorized(err):
		return false, nil
	default:
		return false, err
	}
}

// LogoutResult is what Logout did.
type LogoutResult struct {
	// Stored says the account had a token. Without one there was nothing
	// to do.
	Stored bool
	// NotRevoked is why the session may still exist on the server: the
	// server couldn't be asked, or the stored entry wasn't a token that
	// could be sent. Nil when the server ended the session, or it had
	// ended already.
	NotRevoked error
}

// Logout signs the account out: the server is asked to end the session
// first, then the token is deleted from the store. The token is deleted
// whatever the server answered, or didn't: a terminal that was told to
// sign out holds no token afterwards. LogoutResult.NotRevoked then says
// the session may live on, to revoke from the web's sessions page.
//
// The error is the store's: the token couldn't be deleted.
func Logout(ctx context.Context, s Store, a Account) (LogoutResult, error) {
	if err := a.check(); err != nil {
		return LogoutResult{}, err
	}
	token, err := s.Get(a)
	switch {
	case errors.Is(err, ErrNotFound):
		return LogoutResult{}, nil
	case errors.Is(err, ErrNoKeyring):
		return LogoutResult{}, err
	case err != nil:
		// Not a token that can be sent anywhere: all there is to do is
		// clear the entry.
		res := LogoutResult{Stored: true, NotRevoked: fmt.Errorf("the stored token can't be read: %w", err)}
		return res, s.Delete(a)
	}
	res := LogoutResult{Stored: true}
	c, err := client.New(a.Server, token)
	if err != nil {
		res.NotRevoked = err
	} else if _, err := revoke(ctx, c); err != nil {
		res.NotRevoked = err
	}
	return res, drop(s, a, token)
}
