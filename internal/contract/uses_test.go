// Package contract checks the client against the server's API v1
// contract file, api/testdata/api-v1-contract.txt in the pinned
// github.com/Loomux/server module (the design's "Repository": "A route
// and field check").
package contract

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// usesFile lists the contract lines the client depends on, in the
// contract file's own format. It arrives with the typed client in T3
// (LOOM-282). Until then its absence passes; T3 makes it mandatory.
const usesFile = "../../client/testdata/uses.txt"

// Every line of uses.txt is a line of the server's contract file. The
// contract is additions-only within v1, so a client whose lines are all in
// its pinned server's contract works against every later v1 server.
//
// The server is found with `go list -m`, so a local go.work that points at
// a server checkout checks against that checkout, and the nightly run
// (.github/workflows/server-main.yml) against the server's main.
func TestUsesAreInServerContract(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/Loomux/server")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m github.com/Loomux/server: %v", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatal("go list -m github.com/Loomux/server: no module directory (run go mod download)")
	}
	contract, err := os.ReadFile(filepath.Join(dir, "api", "testdata", "api-v1-contract.txt"))
	if err != nil {
		t.Fatalf("the pinned server's contract file: %v", err)
	}
	if len(lines(contract)) == 0 {
		t.Fatal("the pinned server's contract file has no lines")
	}

	uses, err := os.ReadFile(usesFile)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s doesn't exist yet: it arrives with the typed client (T3, LOOM-282)", usesFile)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(lines(uses)) == 0 {
		t.Errorf("%s lists no contract lines", usesFile)
	}
	for _, l := range missing(contract, uses) {
		t.Errorf("%s: %q is not in the server's API v1 contract (%s)", usesFile, l, dir)
	}
}

// missing returns the lines of uses that aren't lines of contract.
func missing(contract, uses []byte) []string {
	have := map[string]bool{}
	for _, l := range lines(contract) {
		have[l] = true
	}
	var out []string
	for _, l := range lines(uses) {
		if !have[l] {
			out = append(out, l)
		}
	}
	return out
}

// lines returns the contract lines of a file in the contract's format:
// every line but blank ones and "#" comments, trailing space trimmed.
func lines(b []byte) []string {
	var out []string
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		l := strings.TrimRight(s.Text(), " \t\r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestMissing(t *testing.T) {
	contract := []byte(`# API v1 contract
#
GET /api/v1/version
GET /api/v1/version response .api_version string
GET /api/v1/version status 200
`)
	for _, tc := range []struct {
		name string
		uses string
		want []string
	}{
		{"all present", "GET /api/v1/version\nGET /api/v1/version status 200\n", nil},
		{"comments and blank lines ignored", "# what T3 reads\n\nGET /api/v1/version status 200\n", nil},
		{"CRLF and trailing space", "GET /api/v1/version status 200 \r\n", nil},
		{"a missing field", "GET /api/v1/version response .api_version string\nGET /api/v1/version response .server string\n",
			[]string{"GET /api/v1/version response .server string"}},
		{"a changed kind", "GET /api/v1/version response .api_version number\n",
			[]string{"GET /api/v1/version response .api_version number"}},
		{"a prefix of a line isn't a line", "GET /api/v1/version response\n",
			[]string{"GET /api/v1/version response"}},
		{"the contract's comments aren't lines", "API v1 contract\n", []string{"API v1 contract"}},
	} {
		got := missing(contract, []byte(tc.uses))
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s: missing = %q, want %q", tc.name, got, tc.want)
		}
	}
}
