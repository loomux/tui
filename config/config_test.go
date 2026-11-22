package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Loomux/tui/config/keymap"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		goos string
		env  map[string]string
		home string
		want string
	}{
		{"XDG_CONFIG_HOME", "linux", map[string]string{"XDG_CONFIG_HOME": "/x/conf"}, "/home/u", "/x/conf/loomux/config.toml"},
		{"linux default", "linux", nil, "/home/u", "/home/u/.config/loomux/config.toml"},
		{"XDG_CONFIG_HOME on macOS too", "darwin", map[string]string{"XDG_CONFIG_HOME": "/x/conf"}, "/Users/u", "/x/conf/loomux/config.toml"},
		{"macOS default", "darwin", nil, "/Users/u", "/Users/u/Library/Application Support/loomux/config.toml"},
		// The XDG spec: a relative path is invalid and ignored.
		{"relative XDG_CONFIG_HOME ignored", "linux", map[string]string{"XDG_CONFIG_HOME": "conf"}, "/home/u", "/home/u/.config/loomux/config.toml"},
	} {
		got, err := path(env(tc.env), tc.goos, func() (string, error) { return tc.home, nil })
		if err != nil || got != filepath.FromSlash(tc.want) {
			t.Errorf("%s: path = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := path(env(nil), "linux", func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Error("no XDG_CONFIG_HOME and no home directory: want an error")
	}
}

const full = `
default_profile = "test"
token_store = "keyring"  # or "file"

[profiles.test]
server = "https://loomux.example.net"

[profiles.prod]
server = "https://loomux.example.org/"
warn = true

[attach]
mode = "auto"
prefer = ["kubectl", "ssh", "docker"]

[attach.kubectl]
context = ""

[keys]
attach = "A"
send = "ctrl+s"
`

// The design's example config parses as written.
func TestParseFull(t *testing.T) {
	c, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultProfile != "test" || c.TokenStore != TokenStoreKeyring {
		t.Errorf("default_profile %q, token_store %q", c.DefaultProfile, c.TokenStore)
	}
	if p := c.Profiles["prod"]; p.Server != "https://loomux.example.org" || !p.Warn {
		t.Errorf("profiles.prod = %+v: want the normalised server and warn", p)
	}
	if p := c.Profiles["test"]; p.Server != "https://loomux.example.net" || p.Warn {
		t.Errorf("profiles.test = %+v", p)
	}
	if c.Attach.Mode != "auto" || strings.Join(c.Attach.Prefer, ",") != "kubectl,ssh,docker" {
		t.Errorf("attach = %+v", c.Attach)
	}
	if !slices.Equal(c.Keys["send"], []string{"ctrl+s"}) || !slices.Equal(c.Keys["attach"], []string{"A"}) {
		t.Errorf("keys = %v", c.Keys)
	}
	if got := c.Keymap.Keys(keymap.Attach); !slices.Equal(got, []string{"A"}) {
		t.Errorf("keymap attach = %q, want the [keys] override", got)
	}
}

// A [keys] entry is a key or a list of keys.
func TestParseKeys(t *testing.T) {
	c, err := Parse([]byte("[keys]\ncancel = [\"x\", \"ctrl-c\"]\nnew = \"N\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Keymap.Keys(keymap.Cancel); !slices.Equal(got, []string{"x", "ctrl+c"}) {
		t.Errorf("cancel = %q", got)
	}
	if a, _, ok := c.Keymap.Lookup(keymap.ListScreen, "N"); !ok || a != keymap.NewConversation {
		t.Errorf(`"N" on the list = %q, %v`, a, ok)
	}
}

func TestParseDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenStore != TokenStoreKeyring {
		t.Errorf("token_store defaults to %q, want keyring", c.TokenStore)
	}
	if c.Attach.Mode != "auto" {
		t.Errorf("attach.mode defaults to %q, want auto", c.Attach.Mode)
	}
	if len(c.Profiles) != 0 {
		t.Errorf("profiles = %v", c.Profiles)
	}
	for _, a := range keymap.Actions() {
		if !slices.Equal(c.Keymap.Keys(a), keymap.Default().Keys(a)) {
			t.Errorf("keymap %s = %q, want the default", a, c.Keymap.Keys(a))
		}
	}
}

func TestParseFileStore(t *testing.T) {
	c, err := Parse([]byte(`token_store = "file"`))
	if err != nil || c.TokenStore != TokenStoreFile {
		t.Errorf("token_store = file: %+v, %v", c, err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"not TOML", `default_profile = `, "config"},
		{"unknown top-level key", `tokens_store = "file"`, `unknown key "tokens_store"`},
		{"unknown profile key", "[profiles.test]\nserver = \"https://example.net\"\nwarning = true\n", `unknown key "profiles.test.warning"`},
		{"unknown section", "[colours]\nheader = \"red\"\n", `unknown key "colours`},
		{"a token in the config", "[profiles.test]\nserver = \"https://example.net\"\ntoken = \"tok-secret\"\n", "never holds a token"},
		{"a top-level token", `token = "tok-secret"`, "never holds a token"},
		{"bad token_store", `token_store = "plain"`, `token_store: use "keyring"`},
		// A value that may be a token pasted in the wrong place isn't
		// repeated.
		{"a token as token_store", `token_store = "tok-secret"`, `token_store: use "keyring"`},
		{"a secret in a server query", "[profiles.test]\nserver = \"https://example.net/?t=tok-secret\"\n", "profiles.test"},
		{"a secret in LOOMUX-style userinfo", "[profiles.test]\nserver = \"https://u:tok-secret@example.net\"\n", "profiles.test"},
		{"http server", "[profiles.test]\nserver = \"http://loomux.example.net\"\n", `profiles.test: server`},
		{"scheme-less server", "[profiles.test]\nserver = \"loomux.example.net\"\n", `https://loomux.example.net`},
		{"no server", "[profiles.test]\nwarn = true\n", `profiles.test: no server`},
		{"bad profile name", "[profiles.\"../x\"]\nserver = \"https://example.net\"\n", `profile name "../x"`},
		{"profile name with @", "[profiles.\"a@b\"]\nserver = \"https://example.net\"\n", `profile name "a@b"`},
		{"default_profile not defined", "default_profile = \"prod\"\n[profiles.test]\nserver = \"https://example.net\"\n", `default_profile "prod"`},
		{"bad attach mode", "[attach]\nmode = \"tab\"\n", `attach.mode "tab"`},
		{"wrong type", `token_store = 1`, "config"},
		// [keys] is checked at start (T14).
		{"unknown action", "[keys]\natach = \"A\"\n", "keys.atach: unknown action"},
		{"two actions, one key", "[keys]\nnew = \"a\"\n", `"a" is both`},
		{"not a key", "[keys]\nattach = \"ctrl+\"\n", "keys.attach"},
		{"a key of the wrong type", "[keys]\nattach = 1\n", "keys.attach: give a key"},
		{"a list of the wrong type", "[keys]\nattach = [\"a\", 1]\n", "keys.attach: give a key"},
		{"a table in keys", "[keys.attach]\nkey = \"a\"\n", "keys.attach: give a key"},
		{"a token in keys", "[keys]\ntoken = \"tok-secret\"\n", "never holds a token"},
		{"a token-named action", "[keys]\napi_token = [\"tok-secret\"]\n", "never holds a token"},
	} {
		_, err := Parse([]byte(tc.toml))
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q doesn't say %q", tc.name, err, tc.want)
		}
		if strings.Contains(err.Error(), "tok-secret") {
			t.Errorf("%s: error %q repeats the token", tc.name, err)
		}
	}
}

// A TOML syntax error on a token line doesn't repeat the line's value.
func TestParseErrorHidesTokenValue(t *testing.T) {
	for _, src := range []string{
		"token = sekritvalue\n",
		"[profiles.test]\nserver = \"https://example.net\"\napi_token = sekritvalue\n",
	} {
		_, err := Parse([]byte(src))
		if err == nil {
			t.Fatalf("%q: no error", src)
		}
		if strings.Contains(err.Error(), "sekrit") {
			t.Errorf("%q: error %q repeats the value", src, err)
		}
		if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%q: error %q doesn't give the line", src, err)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")

	// No file: the defaults, and no error.
	c, err := Load(p)
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if c.TokenStore != TokenStoreKeyring || c.Path != p {
		t.Errorf("missing file: %+v", c)
	}

	if err := os.WriteFile(p, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil || c.DefaultProfile != "test" || c.Path != p {
		t.Fatalf("Load = %+v, %v", c, err)
	}

	if err := os.WriteFile(p, []byte(`token_store = "plain"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("a bad config: error %v doesn't name the file", err)
	}

	// A directory where the file should be is an error, not the defaults.
	if _, err := Load(dir); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(dir) = %v, want a read error", err)
	}
}

func TestSelect(t *testing.T) {
	two := `
default_profile = "test"
[profiles.test]
server = "https://loomux.example.net"
[profiles.prod]
server = "https://loomux.example.org"
warn = true
`
	one := "[profiles.only]\nserver = \"https://loomux.example.net\"\n"
	noDefault := "[profiles.a]\nserver = \"https://a.example.net\"\n[profiles.b]\nserver = \"https://b.example.net\"\n"

	for _, tc := range []struct {
		name   string
		config string
		flag   string
		env    map[string]string
		want   Profile
	}{
		{"default_profile", two, "", nil, Profile{"test", "https://loomux.example.net", false}},
		{"LOOMUX_PROFILE beats default_profile", two, "", map[string]string{"LOOMUX_PROFILE": "prod"}, Profile{"prod", "https://loomux.example.org", true}},
		{"--profile beats LOOMUX_PROFILE", two, "test", map[string]string{"LOOMUX_PROFILE": "prod"}, Profile{"test", "https://loomux.example.net", false}},
		{"LOOMUX_SERVER overrides the URL", two, "prod", map[string]string{"LOOMUX_SERVER": "https://other.example.org/"}, Profile{"prod", "https://other.example.org", true}},
		{"the only profile", one, "", nil, Profile{"only", "https://loomux.example.net", false}},
		{"no config, LOOMUX_SERVER", "", "", map[string]string{"LOOMUX_SERVER": "https://loomux.example.net"}, Profile{"default", "https://loomux.example.net", false}},
		{"no config, LOOMUX_SERVER and a name", "", "", map[string]string{"LOOMUX_SERVER": "https://loomux.example.net", "LOOMUX_PROFILE": "ci"}, Profile{"ci", "https://loomux.example.net", false}},
		{"an unknown profile with LOOMUX_SERVER", two, "staging", map[string]string{"LOOMUX_SERVER": "https://staging.example.net"}, Profile{"staging", "https://staging.example.net", false}},
		{"loopback http", "", "", map[string]string{"LOOMUX_SERVER": "http://127.0.0.1:8080"}, Profile{"default", "http://127.0.0.1:8080", false}},
	} {
		c, err := Parse([]byte(tc.config))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := c.Select(tc.flag, env(tc.env))
		if err != nil || got != tc.want {
			t.Errorf("%s: Select = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	for _, tc := range []struct {
		name   string
		config string
		flag   string
		env    map[string]string
		want   string
	}{
		{"no config", "", "", nil, "no profile"},
		{"several profiles, no default", noDefault, "", nil, "default_profile"},
		{"unknown --profile", two, "staging", nil, `profile "staging"`},
		{"unknown LOOMUX_PROFILE", two, "", map[string]string{"LOOMUX_PROFILE": "staging"}, `profile "staging"`},
		{"http LOOMUX_SERVER", two, "", map[string]string{"LOOMUX_SERVER": "http://loomux.example.net"}, "LOOMUX_SERVER"},
		{"scheme-less LOOMUX_SERVER", two, "", map[string]string{"LOOMUX_SERVER": "loomux.example.net"}, "https://loomux.example.net"},
		{"bad --profile name", "", "../x", map[string]string{"LOOMUX_SERVER": "https://example.net"}, `profile name "../x"`},
	} {
		c, err := Parse([]byte(tc.config))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := c.Select(tc.flag, env(tc.env))
		if err == nil {
			t.Errorf("%s: Select = %+v, want an error", tc.name, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q doesn't say %q", tc.name, err, tc.want)
		}
	}
}
