package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Loomux/tui/config"
)

// No test reaches the real keyring: every one runs against go-keyring's
// in-memory mock, or a mock that fails.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

const token = "q8yG0f2bS1v4mJ0xZt7KcW9hN3eLr5aPdU6iO2sYxQk" // the shape of a real one: 32 bytes, base64url

var acct = Account{Profile: "test", Server: "https://loomux.example.net"}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestAccountString(t *testing.T) {
	if got := acct.String(); got != "test@https://loomux.example.net" {
		t.Errorf("Account.String = %q", got)
	}
}

func TestStateDir(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_STATE_HOME": "/x/state"}, "/x/state/loomux"},
		{nil, "/home/u/.local/state/loomux"},
		{map[string]string{"XDG_STATE_HOME": "rel"}, "/home/u/.local/state/loomux"},
	} {
		got, err := stateDir(env(tc.env), home)
		if err != nil || got != filepath.FromSlash(tc.want) {
			t.Errorf("stateDir(%v) = %q, %v; want %q", tc.env, got, err, tc.want)
		}
	}
	if _, err := stateDir(env(nil), func() (string, error) { return "", errors.New("none") }); err == nil {
		t.Error("no XDG_STATE_HOME and no home: want an error")
	}
}

func TestOpen(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*KeyringStore); !ok {
		t.Errorf("default token_store: Open = %T, want the keyring", s)
	}
	if w := Warning(s, acct); w != "" {
		t.Errorf("keyring: Warning = %q, want none", w)
	}

	c, err = config.Parse([]byte(`token_store = "file"`))
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := s.(*FileStore)
	if !ok || fs.Dir != filepath.Join(state, "loomux") {
		t.Fatalf("token_store = file: Open = %#v, want a FileStore in %s/loomux", s, state)
	}
	w := Warning(s, acct)
	if !strings.Contains(w, "plain file") || !strings.Contains(w, fs.path(acct)) || !strings.Contains(w, `"test"`) || strings.Contains(w, "\n") {
		t.Errorf("file store: Warning = %q, want one line naming the profile and the file", w)
	}
}

// Both stores, through the Store interface.
func stores(t *testing.T) map[string]Store {
	return map[string]Store{
		"keyring": NewKeyringStore("/x/config.toml"),
		"file":    NewFileStore(filepath.Join(t.TempDir(), "loomux")),
	}
}

func TestStoreRoundTrip(t *testing.T) {
	for name, s := range stores(t) {
		other := Account{Profile: "prod", Server: "https://loomux.example.org"}
		if _, err := s.Get(acct); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Get before Set = %v, want ErrNotFound", name, err)
		}
		if err := s.Delete(acct); err != nil {
			t.Errorf("%s: Delete of nothing = %v, want nil", name, err)
		}
		if err := s.Set(acct, token); err != nil {
			t.Fatalf("%s: Set: %v", name, err)
		}
		if err := s.Set(other, token+"x"); err != nil {
			t.Fatalf("%s: Set other: %v", name, err)
		}
		if got, err := s.Get(acct); err != nil || got != token {
			t.Errorf("%s: Get = %q, %v", name, got, err)
		}
		// A new login replaces the token.
		if err := s.Set(acct, token+"2"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(acct); got != token+"2" {
			t.Errorf("%s: Get after a second Set = %q", name, got)
		}
		if err := s.Delete(acct); err != nil {
			t.Errorf("%s: Delete = %v", name, err)
		}
		if _, err := s.Get(acct); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Get after Delete = %v, want ErrNotFound", name, err)
		}
		if got, err := s.Get(other); err != nil || got != token+"x" {
			t.Errorf("%s: the other profile's token after Delete = %q, %v", name, got, err)
		}
		_ = s.Delete(other)
	}
}

// The same profile name on another server is another account: a token is
// never sent to a server it wasn't issued by.
func TestStoreServerChange(t *testing.T) {
	for name, s := range stores(t) {
		if err := s.Set(acct, token); err != nil {
			t.Fatal(err)
		}
		moved := Account{Profile: acct.Profile, Server: "https://elsewhere.example.org"}
		if got, err := s.Get(moved); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Get for the profile on another server = %q, %v; want ErrNotFound", name, got, err)
		}
		_ = s.Delete(acct)
	}
}

// Deleting an account never drops another server's token kept for the
// same profile.
func TestStoreDeleteOnlyItsServer(t *testing.T) {
	for name, s := range stores(t) {
		if err := s.Set(acct, token); err != nil {
			t.Fatal(err)
		}
		old := Account{Profile: acct.Profile, Server: "https://old.example.org"}
		if err := s.Delete(old); err != nil {
			t.Errorf("%s: Delete for the profile's old server = %v", name, err)
		}
		if got, err := s.Get(acct); err != nil || got != token {
			t.Errorf("%s: Delete for another server dropped this one's token: %q, %v", name, got, err)
		}
		_ = s.Delete(acct)
	}
}

func TestStoreRefusesBadInput(t *testing.T) {
	for name, s := range stores(t) {
		for _, bad := range []string{"", "two words", "line\nbreak", "tab\there", "nul\x00", strings.Repeat("a", 5000), "ünïcode"} {
			err := s.Set(acct, bad)
			if err == nil {
				t.Errorf("%s: Set(%q) accepted", name, bad)
				continue
			}
			if bad != "" && strings.Contains(err.Error(), bad) {
				t.Errorf("%s: Set error %q repeats the token", name, err)
			}
		}
		for _, a := range []Account{
			{Profile: "../x", Server: acct.Server},
			{Profile: "", Server: acct.Server},
			{Profile: "a/b", Server: acct.Server},
			{Profile: "test", Server: ""},
		} {
			if err := s.Set(a, token); err == nil {
				t.Errorf("%s: Set for account %+v accepted", name, a)
			}
			if _, err := s.Get(a); err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("%s: Get for account %+v = %v, want a refusal", name, a, err)
			}
		}
	}
}

func TestKeyringAccount(t *testing.T) {
	s := NewKeyringStore("/x/config.toml")
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(acct)
	// Service loomux, account <profile>@<server URL> (the design's
	// "Storage and revocation").
	if got, err := keyring.Get("loomux", "test@https://loomux.example.net"); err != nil || got != token {
		t.Errorf("keyring.Get(loomux, test@https://loomux.example.net) = %q, %v", got, err)
	}
}

// No keyring: every operation fails with ErrNoKeyring and says how to opt
// in to the file store, never falls back on its own, and never repeats
// the token.
func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("org.freedesktop.secrets was not provided by any .service files"))
	defer keyring.MockInit()

	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	s := NewKeyringStore("/x/config.toml")
	_, getErr := s.Get(acct)
	for op, err := range map[string]error{"Get": getErr, "Set": s.Set(acct, token), "Delete": s.Delete(acct)} {
		if !errors.Is(err, ErrNoKeyring) {
			t.Errorf("%s = %v, want ErrNoKeyring", op, err)
			continue
		}
		msg := err.Error()
		for _, want := range []string{`token_store = "file"`, "/x/config.toml", "org.freedesktop.secrets"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s error %q doesn't say %q", op, msg, want)
			}
		}
		if strings.Contains(msg, token) {
			t.Errorf("%s error repeats the token", op)
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the keyring failed and %s now has %v: it fell back to a file", state, entries)
	}
}

func TestFileStoreCorruptFile(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "not json " + token, `{"server":"https://loomux.example.net","token":""}`, `{"server":"https://loomux.example.net","token":"two words"}`} {
		if err := os.WriteFile(s.path(acct), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(acct)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("content %q: Get = %q, %v; want an error", content, got, err)
			continue
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "two words") {
			t.Errorf("content %q: error %q repeats the file", content, err)
		}
	}
}

func TestFileStoreDeleteLeavesOthers(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "loomux"))
	other := Account{Profile: "prod", Server: acct.Server}
	if err := s.Set(acct, token); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(other, token); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(acct); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path(acct)); !os.IsNotExist(err) {
		t.Errorf("the token file is still there after Delete: %v", err)
	}
	if _, err := s.Get(other); err != nil {
		t.Errorf("Delete removed another profile's token: %v", err)
	}
}

// Missing directory: Get is ErrNotFound, and Delete nothing to do.
func TestFileStoreNoDirectory(t *testing.T) {
	s := NewFileStore(filepath.Join(t.TempDir(), "absent", "loomux"))
	if _, err := s.Get(acct); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
	if err := s.Delete(acct); err != nil {
		t.Errorf("Delete = %v", err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("Get or Delete created %s", s.Dir)
	}
}
