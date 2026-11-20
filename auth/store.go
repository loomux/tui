// Package auth keeps the client's API token (the design in
// loomux/server, docs/design/tui-client-design.md, "Storage and
// revocation"). token_store in the config chooses the store:
//
//   - keyring, the default: the OS keyring (Secret Service on Linux, the
//     Keychain on macOS), service "loomux", account <profile>@<server URL>.
//     With no keyring available it fails with ErrNoKeyring, which says how
//     to opt in to the file store. It never falls back on its own.
//   - file, opt-in: a 0600 file per profile in a 0700 directory,
//     $XDG_STATE_HOME/loomux. A file or directory others can read is
//     refused (ErrInsecure), not used. Every start prints Warning.
//
// It also gets the token and gives it up ("The link flow", "Storage and
// revocation"):
//
//   - Login is `loomux login`: the terminal makes its own token (NewToken)
//     and shows a link with the token's hash (TokenHash, LinkURL) and a
//     code (LinkCode). The web's /link page shows the same code, and its
//     Approve asks the server for a session for that hash. The terminal
//     asks the server with the token until it is taken, and only then
//     stores it.
//   - Logout is `loomux logout`: the session is ended on the server, then
//     the token deleted, also when the server couldn't be asked.
//   - SignedOut is the 401 rule: the API's 401 to a request made with the
//     stored token deletes that token.
//
// A token is never printed, logged or put in an error.
package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Loomux/tui/config"
)

var (
	// ErrNotFound: no token is stored for the account (signed out).
	ErrNotFound = errors.New("no stored token")
	// ErrNoKeyring: token_store is keyring and no keyring can be used.
	ErrNoKeyring = errors.New("no keyring")
	// ErrInsecure: the token file or its directory is open to others, or
	// isn't a plain file or directory.
	ErrInsecure = errors.New("insecure token file")
)

// Account is whose token: a profile on a server. The same profile name on
// another server is another account, so a token is only ever sent to the
// server that issued it.
type Account struct {
	Profile string
	Server  string // normalised by config.CheckServer
}

// String is the keyring account: <profile>@<server URL>.
func (a Account) String() string { return a.Profile + "@" + a.Server }

func (a Account) check() error {
	if err := config.CheckProfileName(a.Profile); err != nil {
		return err
	}
	if a.Server == "" {
		return errors.New("no server")
	}
	return nil
}

// AccountFor is the account of a selected profile.
func AccountFor(p config.Profile) Account {
	return Account{Profile: p.Name, Server: p.Server}
}

// Store keeps one token per account.
type Store interface {
	// Get returns the account's token, or ErrNotFound.
	Get(Account) (string, error)
	// Set stores the account's token, replacing any.
	Set(Account, string) error
	// Delete drops the account's token. Nothing stored is no error.
	Delete(Account) error
}

// Open returns the store the config's token_store names.
func Open(c *config.Config) (Store, error) {
	switch c.TokenStore {
	case config.TokenStoreKeyring, "":
		return NewKeyringStore(c.Path), nil
	case config.TokenStoreFile:
		dir, err := StateDir()
		if err != nil {
			return nil, err
		}
		return NewFileStore(dir), nil
	}
	return nil, fmt.Errorf("token_store %q: use %q or %q", c.TokenStore, config.TokenStoreKeyring, config.TokenStoreFile)
}

// Warning is the line every start prints for the store, or "": the file
// store warns that the token is in a plain file.
func Warning(s Store, a Account) string {
	if f, ok := s.(*FileStore); ok {
		return fmt.Sprintf("warning: the token for profile %q is in a plain file, %s (token_store = %q)", a.Profile, f.path(a), config.TokenStoreFile)
	}
	return ""
}

// StateDir is the file store's directory: $XDG_STATE_HOME/loomux, or
// ~/.local/state/loomux without XDG_STATE_HOME.
func StateDir() (string, error) {
	return stateDir(os.Getenv, os.UserHomeDir)
}

func stateDir(getenv func(string) string, home func() (string, error)) (string, error) {
	// The XDG spec ignores a relative XDG_STATE_HOME.
	if dir := getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "loomux"), nil
	}
	h, err := home()
	if err != nil || h == "" {
		return "", errors.New("can't find the token file's directory: no XDG_STATE_HOME and no home directory")
	}
	return filepath.Join(h, ".local", "state", "loomux"), nil
}

// maxToken bounds a stored token. A real one is 43 characters (32 bytes,
// base64url).
const maxToken = 4096

// checkToken refuses what can't be a token: empty, too long, or anything
// but printable ASCII without spaces. The error never repeats it.
func checkToken(tok string) error {
	if tok == "" {
		return errors.New("empty token")
	}
	if len(tok) > maxToken {
		return fmt.Errorf("the token is %d bytes, more than %d", len(tok), maxToken)
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c <= ' ' || c > '~' {
			return errors.New("the token has a character that can't be in one (a space, control or non-ASCII character)")
		}
	}
	return nil
}
