//go:build !unix

package auth

import (
	"io/fs"
	"os"
	"sync"
)

// The file store is for Linux and macOS (Windows support is deferred in
// the design). Elsewhere it has no symlink or owner checks, and the mode
// check refuses what the system reports.

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

func isSymlinkErr(error) bool { return false }

func ownedByMe(fs.FileInfo) bool { return true }

// Windows is deferred in the design: elsewhere the lock only holds
// within this process.
var fileLocks sync.Map // path -> *sync.Mutex

func lockFile(f *os.File) error {
	m, _ := fileLocks.LoadOrStore(f.Name(), &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	return nil
}

func unlockFile(f *os.File) {
	if m, ok := fileLocks.Load(f.Name()); ok {
		m.(*sync.Mutex).Unlock()
	}
}
