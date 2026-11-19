package config

import (
	"strings"
	"testing"
)

func TestCheckServerAccepts(t *testing.T) {
	for raw, want := range map[string]string{
		"https://loomux.example.net":      "https://loomux.example.net",
		"https://loomux.example.net/":     "https://loomux.example.net",
		"HTTPS://Loomux.Example.NET/":     "https://loomux.example.net",
		"https://loomux.example.net:443":  "https://loomux.example.net",
		"https://loomux.example.net:8443": "https://loomux.example.net:8443",
		"https://example.org/loomux/":     "https://example.org/loomux",
		"  https://loomux.example.net  ":  "https://loomux.example.net",
		"https://[2001:db8::1]":           "https://[2001:db8::1]",
		"http://localhost:8080":           "http://localhost:8080",
		"http://LOCALHOST:8080/":          "http://localhost:8080",
		"http://127.0.0.1:8080":           "http://127.0.0.1:8080",
		"http://127.1.2.3":                "http://127.1.2.3",
		"http://[::1]:8080":               "http://[::1]:8080",
		"http://localhost:80":             "http://localhost",
		"https://localhost":               "https://localhost",
	} {
		got, err := CheckServer(raw)
		if err != nil || got != want {
			t.Errorf("CheckServer(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestCheckServerRefuses(t *testing.T) {
	for raw, wantMsg := range map[string]string{
		"":                                "no server URL",
		"http://loomux.example.net":       "https://",
		"HTTP://loomux.example.net":       "https://",
		"http://10.0.0.1":                 "https://",
		"http://0.0.0.0:8080":             "https://",
		"http://localhost.example.net":    "https://",
		"http://127.0.0.1.example.net":    "https://",
		"loomux.example.net":              `https://loomux.example.net`,
		"loomux.example.net:443":          `https://loomux.example.net:443`,
		"loomux.example.net/api":          `https://loomux.example.net/api`,
		"//loomux.example.net":            "https://",
		"localhost:8080":                  "https://",
		"ftp://loomux.example.net":        "https://",
		"ws://loomux.example.net":         "https://",
		"https://":                        "no host",
		"https:///path":                   "no host",
		"https://user:secret@example.net": "user name or password",
		"https://user@example.net":        "user name or password",
		"https://example.net/?x=1":        "query",
		"https://example.net/#frag":       "fragment",
		"https://exa mple.net":            "",
		"https://example.net/api/v1":      "/api/v1",
	} {
		got, err := CheckServer(raw)
		if err == nil {
			t.Errorf("CheckServer(%q) = %q, want an error", raw, got)
			continue
		}
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("CheckServer(%q) error %q doesn't say %q", raw, err, wantMsg)
		}
		// A password in the URL is never repeated back.
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("CheckServer(%q) error %q repeats the password", raw, err)
		}
	}
}
