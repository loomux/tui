package auth

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockAccount takes the exclusive lock for account a in dir, creating dir
// 0700 when it is missing, and returns the unlock. Set, Delete and
// DeleteIf take it, so a compare-and-delete can't be split by a login's
// write, in this process or another. The lock file stays: removing it
// while another process waits on it would let two holders in.
func lockAccount(dir string, a Account) (func(), error) {
	if err := a.check(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("token store lock: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, a.Profile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("token store lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("token store lock: %w", err)
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}
