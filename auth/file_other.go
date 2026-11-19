//go:build !unix

package auth

import (
	"io/fs"
	"os"
)

// The file store is for Linux and macOS (Windows support is deferred in
// the design). Elsewhere it has no symlink or owner checks, and the mode
// check refuses what the system reports.

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

func isSymlinkErr(error) bool { return false }

func ownedByMe(fs.FileInfo) bool { return true }
