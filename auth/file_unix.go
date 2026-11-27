//go:build unix

package auth

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// openNoFollow opens path for reading, failing on a symlink rather than
// reading what it points at. O_NONBLOCK keeps a named pipe from blocking
// the open until a writer appears (the caller's file-type check then
// refuses it); it changes nothing for a regular file.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// isSymlinkErr reports whether openNoFollow failed because the path is a
// symlink (ELOOP; EMLINK on some BSDs).
func isSymlinkErr(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// ownedByMe reports whether the current user owns the file.
func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// fileLocks serialises holders in this process: flock alone orders the
// operations across processes, but not two goroutines here for the race
// detector, and a map in an in-memory store read by both needs it.
var fileLocks sync.Map // path -> *sync.Mutex

// lockFile waits for the file's exclusive advisory lock.
func lockFile(f *os.File) error {
	m, _ := fileLocks.LoadOrStore(f.Name(), &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		m.(*sync.Mutex).Unlock()
		return err
	}
	return nil
}

// unlockFile releases lockFile's lock.
func unlockFile(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if m, ok := fileLocks.Load(f.Name()); ok {
		m.(*sync.Mutex).Unlock()
	}
}
