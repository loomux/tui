package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The link flow's values (the design's "The link flow"). `loomux login`
// makes its own token T and shows the browser only its hash H: the web's
// /link page asks the server for a session for H, and T never leaves the
// machine. Both sides show a code made from H, which the user compares.

// NewToken returns a new token as the server makes its own (api/auth.go
// newToken): 32 bytes from crypto/rand, base64url without padding. It is
// the secret: it goes in the Authorization header and, once approved, in
// the token store, and nowhere else.
func NewToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenHash is what the server stores for a token, and what the link
// carries in its place: hex(sha256(token)). It isn't a secret.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LinkCode is the code the terminal and the /link page both show for a
// token hash, for the user to compare before approving: the first 8
// characters of base32(sha256("loomux-link-v1:" + hash)), as ABCD-EFGH.
func LinkCode(hash string) string {
	sum := sha256.Sum256([]byte("loomux-link-v1:" + hash))
	// Five bytes are exactly eight base32 characters.
	code := base32.StdEncoding.EncodeToString(sum[:5])
	return code[:4] + "-" + code[4:]
}

// maxLabel is the longest label the server and the /link page take, in
// characters.
const maxLabel = 64

// noLabel is the label of a machine whose host name leaves nothing to
// show.
const noLabel = "terminal"

// LinkLabel makes the session's label from the machine's host name: what
// the sessions page shows for this terminal. The server and the /link
// page refuse a label over 64 characters or with a control character,
// rather than fix it, so it is cut and cleaned here: only printable
// characters stay, which also drops invisible ones (bidi overrides,
// zero-width) that could make the label read as something else.
func LinkLabel(host string) string {
	var b strings.Builder
	for _, r := range host {
		if r != utf8.RuneError && unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	label := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(label) > maxLabel {
		label = strings.TrimSpace(string([]rune(label)[:maxLabel]))
	}
	if label == "" {
		return noLabel
	}
	return label
}

// LinkURL is the link `loomux login` shows and opens: server's /link page
// with the token hash and the label in the fragment, which a browser
// never sends, so they reach no proxy or server log. server is a root URL
// as config.CheckServer returns it.
func LinkURL(server, hash, label string) string {
	return server + "/link#" + url.Values{"h": {hash}, "label": {label}}.Encode()
}
