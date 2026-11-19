//go:build unix

package auth

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
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
