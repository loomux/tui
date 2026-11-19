package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileStore keeps one token per profile in a 0600 file, <profile>.token,
// in a 0700 directory: the opt-in store for a machine with no keyring
// (token_store = "file"). A file or directory with wider permissions, or
// one that is a symlink or someone else's, is refused, not used.
type FileStore struct {
	Dir string
}

// NewFileStore returns the file store in dir (StateDir for the real one).
func NewFileStore(dir string) *FileStore { return &FileStore{Dir: dir} }

// tokenFile is the file's contents. The server it was issued for is kept
// with it, so a profile pointed at another server doesn't send it there.
type tokenFile struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func (s *FileStore) path(a Account) string {
	return filepath.Join(s.Dir, a.Profile+".token")
}

func (s *FileStore) Get(a Account) (string, error) {
	if err := a.check(); err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if err := s.checkDir(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	p := s.path(a)
	f, err := openNoFollow(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		if isSymlinkErr(err) {
			return "", fmt.Errorf("%w: %s is a symlink, not a plain file: delete it and run `loomux login`", ErrInsecure, p)
		}
		return "", fmt.Errorf("token file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if err := checkPrivate(p, fi, 0o600, "chmod 600 "+p); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, 2*maxToken))
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	var tf tokenFile
	// The decoder's errors can quote the file, so they aren't passed on.
	if json.Unmarshal(b, &tf) != nil || checkToken(tf.Token) != nil || tf.Server == "" {
		return "", fmt.Errorf("token file %s isn't valid: delete it and run `loomux login`", p)
	}
	if tf.Server != a.Server {
		// Issued by another server: the profile has moved.
		return "", ErrNotFound
	}
	return tf.Token, nil
}

func (s *FileStore) Set(a Account, tok string) error {
	if err := a.check(); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := checkToken(tok); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := s.checkDir(); err != nil {
		return err
	}
	b, err := json.Marshal(tokenFile{Server: a.Server, Token: tok})
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	// Written to a temporary file, created 0600, and renamed over the old
	// one: never a partly written token, and never a moment at a wider
	// mode. A rename replaces a symlink rather than writing through it.
	tmp, err := os.CreateTemp(s.Dir, "."+a.Profile+".token.*")
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := os.Rename(name, s.path(a)); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	ok = true
	return nil
}

// Delete removes the profile's token file, unless it holds another
// server's token: that is another account's. An unreadable or corrupt
// file is no one's token and is removed.
func (s *FileStore) Delete(a Account) error {
	if err := a.check(); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	p := s.path(a)
	if f, err := openNoFollow(p); err == nil {
		var tf tokenFile
		b, rerr := io.ReadAll(io.LimitReader(f, 2*maxToken))
		f.Close()
		if rerr == nil && json.Unmarshal(b, &tf) == nil && tf.Server != "" && tf.Server != a.Server {
			return nil
		}
	}
	err := os.Remove(p)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("token file: %w", err)
}

// checkDir refuses a directory that isn't a plain one of the user's own
// with no access for others.
func (s *FileStore) checkDir() error {
	fi, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	// Lstat: a symlink, even to a directory, isn't one.
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s isn't a directory (a symlink to one is refused too)", ErrInsecure, s.Dir)
	}
	return checkPrivate(s.Dir, fi, 0o700, "chmod 700 "+s.Dir)
}

// checkPrivate refuses a file or directory that others can read or write,
// that isn't regular (for a file), or that belongs to another user.
func checkPrivate(path string, fi fs.FileInfo, want fs.FileMode, fix string) error {
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s isn't a plain file: delete it and run `loomux login`", ErrInsecure, path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s is mode %04o, open to other users; it must be %04o, and it was refused, not used. Run `%s`, or delete it and run `loomux login`", ErrInsecure, path, perm, want, fix)
	}
	if !ownedByMe(fi) {
		return fmt.Errorf("%w: %s belongs to another user", ErrInsecure, path)
	}
	return nil
}
