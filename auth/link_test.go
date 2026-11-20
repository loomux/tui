package auth

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The link flow's shared test vector: the same values are in loomux/web's
// src/lib/link.test.ts, so the terminal and the /link page compute the
// same code for the same token. The token is bytes 0x00–0x1f, base64url.
const (
	vectorToken = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	vectorHash  = "ea866a757e4c38babfa8127cbe9a409d3e1f93a00ff1488ff735fcf917afffd0"
	vectorCode  = "7ESB-UHZP"
)

func TestSharedVector(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	if got := base64.RawURLEncoding.EncodeToString(raw); got != vectorToken {
		t.Fatalf("the vector's token = %q, want bytes 0x00-0x1f in base64url", got)
	}
	if got := TokenHash(vectorToken); got != vectorHash {
		t.Errorf("TokenHash = %q, want %q", got, vectorHash)
	}
	if got := LinkCode(vectorHash); got != vectorCode {
		t.Errorf("LinkCode = %q, want %q", got, vectorCode)
	}
}

func TestLinkCode(t *testing.T) {
	for hash, want := range map[string]string{
		vectorHash:              vectorCode,
		strings.Repeat("0", 64): "VS4Z-3W2A",
		"225f7e75329dd45aa354975d73987319309393af3a4c6733bc13601a4f1b8796": "HLE5-QIHC",
	} {
		if got := LinkCode(hash); got != want {
			t.Errorf("LinkCode(%s) = %q, want %q", hash, got, want)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b {
		t.Fatal("two tokens are the same")
	}
	for _, tok := range []string{a, b} {
		// As the server makes them (api/auth.go newToken): 32 random
		// bytes, base64url without padding.
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) != 32 || len(tok) != 43 {
			t.Errorf("a token of %d characters decoding to %d bytes (%v), want 43 and 32", len(tok), len(raw), err)
		}
		if err := checkToken(tok); err != nil {
			t.Errorf("the stores would refuse a new token: %v", err)
		}
	}
}

// serverAcceptsLabel is the server's cleanClientLabel (api/holds.go) and
// the /link page's cleanLabel: the label unchanged by a trim, valid
// UTF-8, at most 64 characters, no control characters.
func serverAcceptsLabel(label string) bool {
	if label != strings.TrimSpace(label) || !utf8.ValidString(label) || utf8.RuneCountInString(label) > 64 {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func TestLinkLabel(t *testing.T) {
	for _, tc := range []struct{ name, host, want string }{
		{"a host name", "laptop", "laptop"},
		{"an FQDN", "laptop.example.net", "laptop.example.net"},
		{"spaces around it", "  laptop \n", "laptop"},
		{"a space inside stays", "my laptop", "my laptop"},
		{"control characters are dropped", "lap\x1b[31mtop\x00\x7f\u0085", "lap[31mtop"},
		{"bidi and other format characters are dropped", "lap‮top​", "laptop"},
		{"invalid UTF-8 is dropped", "lap\xfftop", "laptop"},
		{"cut at 64 characters", strings.Repeat("a", 65) + ".example.net", strings.Repeat("a", 64)},
		{"64 characters fit", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"characters, not bytes", strings.Repeat("é", 70), strings.Repeat("é", 64)},
		{"a cut that ends in a space is trimmed again", strings.Repeat("a", 63) + " b", strings.Repeat("a", 63)},
		{"nothing left", " \x00\t", "terminal"},
		{"no host name", "", "terminal"},
	} {
		got := LinkLabel(tc.host)
		if got != tc.want {
			t.Errorf("%s: LinkLabel(%q) = %q, want %q", tc.name, tc.host, got, tc.want)
		}
		if !serverAcceptsLabel(got) {
			t.Errorf("%s: the server and the page would refuse the label %q", tc.name, got)
		}
	}
}

func TestLinkURL(t *testing.T) {
	got := LinkURL("https://loomux.example.net", vectorHash, "my laptop")
	want := "https://loomux.example.net/link#h=" + vectorHash + "&label=my+laptop"
	if got != want {
		t.Errorf("LinkURL = %q, want %q", got, want)
	}
	// A server under a path keeps it.
	if got := LinkURL("https://example.net/loomux", vectorHash, "laptop"); got != "https://example.net/loomux/link#h="+vectorHash+"&label=laptop" {
		t.Errorf("LinkURL under a path = %q", got)
	}
}

// The page reads the fragment as URLSearchParams do: what it gets back is
// the hash and the label, whatever characters the label has.
func TestLinkURLFragmentRoundTrip(t *testing.T) {
	for _, label := range []string{"laptop", "my laptop", "a&h=evil", "50%+1=#?/", "bücher-ü", "a+b"} {
		link := LinkURL("https://loomux.example.net", vectorHash, label)
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("%q: %v", link, err)
		}
		if u.Path != "/link" || u.RawQuery != "" {
			t.Errorf("%q: path %q, query %q: the link is all in the fragment", link, u.Path, u.RawQuery)
		}
		frag := link[strings.Index(link, "#")+1:]
		if strings.Contains(frag, "#") || strings.Contains(frag, " ") {
			t.Errorf("%q: a raw # or space in the fragment", link)
		}
		q, err := url.ParseQuery(frag)
		if err != nil {
			t.Fatalf("%q: %v", link, err)
		}
		if len(q) != 2 || len(q["h"]) != 1 || q.Get("h") != vectorHash || len(q["label"]) != 1 || q.Get("label") != label {
			t.Errorf("%q: the fragment reads as %v, want h and label %q once each", link, q, label)
		}
	}
}
