package auth

import (
	"errors"
	"fmt"

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
	if err := keyring.Set(keyringService, a.String(), tok); err != nil {
		return s.unavailable(err)
	}
	return nil
}

func (s *KeyringStore) Delete(a Account) error {
	if err := a.check(); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
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
