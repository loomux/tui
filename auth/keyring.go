package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// keyringService is the keyring service every token is stored under.
const keyringService = "loomux"

// KeyringStore keeps tokens in the OS keyring through
// github.com/zalando/go-keyring (pure Go, no cgo).
type KeyringStore struct {
	// configPath is named in ErrNoKeyring's advice.
	configPath string
}

// NewKeyringStore returns the keyring store. configPath is the config
// file the advice on ErrNoKeyring names.
func NewKeyringStore(configPath string) *KeyringStore {
	return &KeyringStore{configPath: configPath}
}

func (s *KeyringStore) Get(a Account) (string, error) {
	if err := a.check(); err != nil {
		return "", fmt.Errorf("keyring: %w", err)
	}
	tok, err := keyring.Get(keyringService, a.String())
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", s.unavailable(err)
	}
	if err := checkToken(tok); err != nil {
		return "", fmt.Errorf("%w: the keyring's entry for %s (%v): run `loomux login` to replace it", ErrInvalid, a, err)
	}
	return tok, nil
}

func (s *KeyringStore) Set(a Account, tok string) error {
	if err := a.check(); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	if err := checkToken(tok); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	unlock, err := s.lock(a)
	if err != nil {
		return err
	}
	defer unlock()
	if err := keyring.Set(keyringService, a.String(), tok); err != nil {
		return s.unavailable(err)
	}
	return nil
}

func (s *KeyringStore) Delete(a Account) error {
	if err := a.check(); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	unlock, err := s.lock(a)
	if err != nil {
		return err
	}
	defer unlock()
	return s.delete(a)
}

// DeleteIf removes the account's token from the keyring if it still is
// token. The read and the delete are one step under the store's lock, so
// a login that stores another token meanwhile keeps it.
func (s *KeyringStore) DeleteIf(a Account, token string) (bool, error) {
	return s.deleteIf(a, token, nil)
}

// deleteIf is DeleteIf; between, when not nil, runs after the stored
// token matched and before it is deleted, for the test that interleaves
// a login. It runs with the lock held, so a login there waits.
func (s *KeyringStore) deleteIf(a Account, token string, between func()) (bool, error) {
	if err := a.check(); err != nil {
		return false, fmt.Errorf("keyring: %w", err)
	}
	unlock, err := s.lock(a)
	if err != nil {
		return false, err
	}
	defer unlock()
	stored, err := s.Get(a)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case stored != token:
		return false, nil
	}
	if between != nil {
		between()
	}
	if err := s.delete(a); err != nil {
		return false, err
	}
	return true, nil
}

// lock is the store's lock for the account. The keyring has no
// compare-and-swap, so the lock file is what makes DeleteIf one step
// against a login in another process.
func (s *KeyringStore) lock(a Account) (func(), error) {
	dir, err := keyringLockDir()
	if err != nil {
		return nil, err
	}
	return lockAccount(dir, a)
}

// keyringLockDir is where the keyring's locks live: a fixed per-user
// path, from the home directory. It can't come from XDG_STATE_HOME: the
// keyring account is the same whichever state directory a process was
// given, so two processes must take the same lock for it.
func keyringLockDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("keyring: token store lock: no home directory")
	}
	return filepath.Join(home, ".local", "state", "loomux", ".locks"), nil
}

// delete removes the keyring entry, its caller holding the lock.
func (s *KeyringStore) delete(a Account) error {
	err := keyring.Delete(keyringService, a.String())
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return s.unavailable(err)
}

// unavailable wraps a keyring failure in ErrNoKeyring with the way to opt
// in to the file store. The keyring's errors come from D-Bus or the
// Keychain and never hold the secret.
func (s *KeyringStore) unavailable(err error) error {
	where := s.configPath
	if where == "" {
		where = "the config file"
	}
	return &keyringError{err: err, where: where}
}

type keyringError struct {
	err   error
	where string
}

func (e *keyringError) Error() string {
	return fmt.Sprintf("the keyring can't be used (%v). loomux doesn't fall back to a file on its own: to keep the token in a 0600 file instead, set token_store = \"file\" in %s", e.err, e.where)
}

func (e *keyringError) Is(target error) bool { return target == ErrNoKeyring }

func (e *keyringError) Unwrap() error { return e.err }
