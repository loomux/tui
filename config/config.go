// Package config reads the client's config file and selects a profile
// (the design in loomux/server, docs/design/tui-client-design.md,
// "Config"):
//
//	default_profile = "test"
//	token_store = "keyring"  # or "file": opt-in 0600 token file where there's no keyring
//
//	[profiles.test]
//	server = "https://loomux.example.net"
//
//	[profiles.prod]
//	server = "https://loomux.example.org"
//	warn = true
//
// The config never holds a token: token_store only chooses where the
// token is kept (package auth).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// The token stores (token_store).
const (
	TokenStoreKeyring = "keyring" // the OS keyring, the default
	TokenStoreFile    = "file"    // an opt-in 0600 file per profile
)

// DefaultProfileName names the profile LOOMUX_SERVER makes when nothing
// else names one.
const DefaultProfileName = "default"

// Config is the parsed config file.
type Config struct {
	DefaultProfile string                   `toml:"default_profile"`
	TokenStore     string                   `toml:"token_store"`
	Profiles       map[string]ProfileConfig `toml:"profiles"`
	Attach         Attach                   `toml:"attach"`
	// Keys maps action names to keys. T14 (the keymap) validates it.
	Keys map[string]string `toml:"keys"`

	// Path is the file the config was read from ("" for Parse).
	Path string `toml:"-"`
}

// ProfileConfig is one [profiles.<name>] table.
type ProfileConfig struct {
	// Server is the server's root URL, normalised by CheckServer.
	Server string `toml:"server"`
	// Warn marks a production profile: a coloured header, and send,
	// attach, watch and take back ask first (T15).
	Warn bool `toml:"warn"`
}

// Attach is the [attach] table, used by attach (T8, T9).
type Attach struct {
	Mode    string   `toml:"mode"` // auto (the default), inline or window
	Prefer  []string `toml:"prefer"`
	Kubectl struct {
		Context string `toml:"context"`
	} `toml:"kubectl"`
}

var attachModes = []string{"auto", "inline", "window"}

// Path returns the config file's path: $XDG_CONFIG_HOME/loomux/config.toml,
// or, without XDG_CONFIG_HOME, ~/.config/loomux/config.toml
// (~/Library/Application Support/loomux/config.toml on macOS).
func Path() (string, error) {
	return path(os.Getenv, runtime.GOOS, os.UserHomeDir)
}

func path(getenv func(string) string, goos string, home func() (string, error)) (string, error) {
	// The XDG spec ignores a relative XDG_CONFIG_HOME.
	if dir := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "loomux", "config.toml"), nil
	}
	h, err := home()
	if err != nil || h == "" {
		return "", fmt.Errorf("can't find the config file: no XDG_CONFIG_HOME and no home directory")
	}
	if goos == "darwin" {
		return filepath.Join(h, "Library", "Application Support", "loomux", "config.toml"), nil
	}
	return filepath.Join(h, ".config", "loomux", "config.toml"), nil
}

// Load reads and checks the config file at path. A file that doesn't
// exist is an empty config: the defaults, with no profiles.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("config: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	return c, nil
}

// Parse parses and checks a config file's contents. Unknown keys are
// errors, not ignored, so a typo doesn't silently change nothing.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	md, err := toml.Decode(string(data), c)
	if err != nil {
		return nil, parseError(err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		if isTokenKey(k) {
			return nil, fmt.Errorf("config: %q: the config never holds a token: remove it (`loomux login` keeps the token in the token store)", k.String())
		}
		unknown = append(unknown, fmt.Sprintf("%q", k.String()))
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("config: unknown key %s", strings.Join(unknown, ", "))
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// isTokenKey reports whether a key's last part names a token ("token",
// "api_token"), as opposed to token_store or a typo of it.
func isTokenKey(k toml.Key) bool {
	if len(k) == 0 {
		return false
	}
	last := strings.ToLower(k[len(k)-1])
	return last == "token" || strings.HasSuffix(last, "_token") || strings.HasSuffix(last, "-token")
}

// parseError reports a TOML error by line, without the parser's
// description when the key may hold a token: it can quote the value.
func parseError(err error) error {
	var pe toml.ParseError
	if errors.As(err, &pe) {
		if strings.Contains(strings.ToLower(pe.LastKey), "token") {
			return fmt.Errorf("config: line %d: invalid TOML near key %q (the value isn't shown: the config never holds a token)", pe.Position.Line, pe.LastKey)
		}
		return fmt.Errorf("config: line %d: %s", pe.Position.Line, pe.Message)
	}
	// A value of the wrong type, which never quotes the value.
	return fmt.Errorf("config: %s", strings.TrimPrefix(err.Error(), "toml: "))
}

// A profile name is a key in the keyring account (<profile>@<server>) and
// the token file's name, so it is kept to letters, digits, ".", "_" and
// "-", starting with a letter or digit.
var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CheckProfileName says whether name can name a profile.
func CheckProfileName(name string) error {
	if !profileName.MatchString(name) {
		return fmt.Errorf("profile name %q: use letters, digits, \".\", \"_\" and \"-\", starting with a letter or digit (at most 64)", name)
	}
	return nil
}

func (c *Config) check() error {
	switch c.TokenStore {
	case "":
		c.TokenStore = TokenStoreKeyring
	case TokenStoreKeyring, TokenStoreFile:
	default:
		return fmt.Errorf("token_store %q: use %q (the default) or %q", c.TokenStore, TokenStoreKeyring, TokenStoreFile)
	}
	if c.Attach.Mode == "" {
		c.Attach.Mode = "auto"
	}
	if !slices.Contains(attachModes, c.Attach.Mode) {
		return fmt.Errorf("attach.mode %q: use one of %s", c.Attach.Mode, strings.Join(attachModes, ", "))
	}
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := CheckProfileName(name); err != nil {
			return err
		}
		p := c.Profiles[name]
		if strings.TrimSpace(p.Server) == "" {
			return fmt.Errorf("profiles.%s: no server", name)
		}
		server, err := CheckServer(p.Server)
		if err != nil {
			return fmt.Errorf("profiles.%s: %w", name, err)
		}
		p.Server = server
		c.Profiles[name] = p
	}
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("default_profile %q isn't a [profiles.%s] table", c.DefaultProfile, c.DefaultProfile)
		}
	}
	return nil
}

// Profile is the profile a run uses.
type Profile struct {
	Name   string
	Server string // normalised, checked by CheckServer
	Warn   bool
}

// Select chooses the profile for a run. Its name is the --profile flag
// (flag), else LOOMUX_PROFILE, else default_profile, else the config's
// only profile. LOOMUX_SERVER overrides its server URL, and also makes a
// profile that isn't in the config (named "default" when nothing names
// it), for running without a config file.
func (c *Config) Select(flag string, getenv func(string) string) (Profile, error) {
	name, from := flag, "--profile"
	if name == "" {
		name, from = getenv("LOOMUX_PROFILE"), "LOOMUX_PROFILE"
	}
	if name == "" {
		name, from = c.DefaultProfile, "default_profile"
	}
	if name == "" && len(c.Profiles) == 1 {
		for n := range c.Profiles {
			name = n
		}
	}
	envServer := getenv("LOOMUX_SERVER")
	if name == "" {
		switch {
		case envServer != "":
			name = DefaultProfileName
		case len(c.Profiles) == 0:
			return Profile{}, fmt.Errorf("no profile: add a [profiles.<name>] table with a server to %s, or set LOOMUX_SERVER", c.where())
		default:
			return Profile{}, fmt.Errorf("several profiles and no default_profile in %s: set default_profile, LOOMUX_PROFILE or --profile", c.where())
		}
	}
	if err := CheckProfileName(name); err != nil {
		return Profile{}, fmt.Errorf("%s: %w", from, err)
	}

	p, ok := c.Profiles[name]
	if !ok && envServer == "" {
		return Profile{}, fmt.Errorf("%s: profile %q isn't in %s", from, name, c.where())
	}
	if envServer != "" {
		server, err := CheckServer(envServer)
		if err != nil {
			return Profile{}, fmt.Errorf("LOOMUX_SERVER: %w", err)
		}
		p.Server = server
	}
	return Profile{Name: name, Server: p.Server, Warn: p.Warn}, nil
}

func (c *Config) where() string {
	if c.Path != "" {
		return c.Path
	}
	return "the config"
}
