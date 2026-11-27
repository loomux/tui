//go:build unix

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFileStoreModes(t *testing.T) {
	// Even with a umask that allows everything, the directory is 0700
	// and the file 0600.
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "state", "loomux")
	s := NewFileStore(dir)
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700|os.ModeDir)
	assertMode(t, s.path(acct), 0o600)
	if s.path(acct) != filepath.Join(dir, "test.token") {
		t.Errorf("path = %q, want one file per profile", s.path(acct))
	}
	// No temporary file is left behind: the token file and the lock
	// directory, nothing else.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 || entries[0].Name() != ".locks" || entries[1].Name() != acct.Profile+".token" {
		t.Errorf("%s holds %v, want the token file and .locks", dir, entries)
	}
	assertMode(t, filepath.Join(dir, ".locks"), 0o700|os.ModeDir)
	// The file holds the server it was issued for, and the token.
	b, _ := os.ReadFile(s.path(acct))
	if !strings.Contains(string(b), acct.Server) {
		t.Errorf("the token file doesn't name its server: %d bytes", len(b))
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != want {
		t.Errorf("%s is %v, want %v", path, fi.Mode(), want)
	}
}

// A token file or directory others can read is refused, not used, and
// left as it is.
func TestFileStoreRefusesWiderPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o606, 0o666, 0o700} {
		s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
		if err := s.Set(acct, token); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(s.path(acct), mode); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(acct)
		if mode&0o077 == 0 {
			// 0700: owner-only, but executable; still nobody else's.
			if err != nil || got != token {
				t.Errorf("mode %04o: Get = %v", mode, err)
			}
			continue
		}
		if !errors.Is(err, ErrInsecure) || got != "" {
			t.Errorf("mode %04o: Get = %q, %v; want ErrInsecure", mode, got, err)
			continue
		}
		if !strings.Contains(err.Error(), "chmod 600 "+s.path(acct)) || strings.Contains(err.Error(), token) {
			t.Errorf("mode %04o: error %q", mode, err)
		}
		assertMode(t, s.path(acct), mode)
	}

	for _, mode := range []os.FileMode{0o755, 0o750, 0o705, 0o777} {
		s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
		if err := s.Set(acct, token); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(s.Dir, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(acct); !errors.Is(err, ErrInsecure) || !strings.Contains(err.Error(), "chmod 700 "+s.Dir) {
			t.Errorf("directory mode %04o: Get = %v, want ErrInsecure", mode, err)
		}
		if err := s.Set(acct, token); !errors.Is(err, ErrInsecure) {
			t.Errorf("directory mode %04o: Set = %v, want ErrInsecure", mode, err)
		}
	}
}

// A new login replaces an insecure file with a 0600 one.
func TestFileStoreSetReplacesInsecureFile(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.path(acct), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(acct, token+"2"); err != nil {
		t.Fatal(err)
	}
	assertMode(t, s.path(acct), 0o600)
	if got, err := s.Get(acct); err != nil || got != token+"2" {
		t.Errorf("Get = %q, %v", got, err)
	}
}

func TestFileStoreRefusesSymlinks(t *testing.T) {
	base := t.TempDir()
	s := NewFileStore(filepath.Join(base, "loomux"))
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	// The token file is a symlink to a 0600 file elsewhere.
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.Rename(s.path(acct), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, s.path(acct)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(acct); !errors.Is(err, ErrInsecure) {
		t.Errorf("a symlinked token file: Get = %v, want ErrInsecure", err)
	}

	// The directory is a symlink.
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ls := NewFileStore(link)
	if err := ls.Set(acct, token); !errors.Is(err, ErrInsecure) {
		t.Errorf("a symlinked directory: Set = %v, want ErrInsecure", err)
	}
}

// A named pipe at the token path is refused or removed, never waited on:
// opening one for reading blocks until a writer appears.
func TestFileStoreFIFO(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.path(acct)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(s.path(acct), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := s.Get(acct)
		done <- err
		done <- s.Delete(acct)
	}()
	for _, op := range []string{"Get", "Delete"} {
		select {
		case err := <-done:
			if op == "Get" && !errors.Is(err, ErrInsecure) {
				t.Errorf("Get = %v, want ErrInsecure", err)
			}
			if op == "Delete" && err != nil {
				t.Errorf("Delete = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s hangs on a named pipe", op)
		}
	}
	if _, err := os.Lstat(s.path(acct)); !os.IsNotExist(err) {
		t.Errorf("Delete left the named pipe: %v", err)
	}
}
