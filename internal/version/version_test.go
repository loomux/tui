package version

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	serverversion "github.com/Loomux/server/version"
)

// The constant go generate writes must match the requirement in go.mod.
// It goes stale when the pin is bumped without `go generate ./...`.
func TestServerPinMatchesGoMod(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/Loomux/server")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m github.com/Loomux/server: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != ServerPin {
		t.Errorf("ServerPin = %q, go.mod requires %q: run go generate ./...", ServerPin, got)
	}
}

// The pin is a server release, read with the server's own parser. This
// import is also what keeps the server requirement in go.mod: only test
// files import the server, so without one `go mod tidy` would drop it.
func TestServerPinIsARelease(t *testing.T) {
	if _, ok := serverversion.Parse(strings.TrimPrefix(ServerPin, "v")); !ok {
		t.Errorf("ServerPin %q is not a server release version", ServerPin)
	}
}

// go.mod pins the server at a release tag: never a pseudo-version, never
// a replace directive (AGENTS.md, "The server pin"; the design's
// "Repository"). A local go.work is the way to develop against an
// unmerged server change; CI runs with GOWORK=off and refuses a committed
// one. The nightly run against the server's main (.github/workflows/
// server-main.yml) skips this test: it may pin a pseudo-version.
func TestGoModPinIsATag(t *testing.T) {
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod struct {
		Require []struct{ Path, Version string }
		Replace []struct {
			Old, New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	for _, r := range mod.Replace {
		t.Errorf("go.mod replaces %s with %s %s: no replace directives (use a local go.work)", r.Old.Path, r.New.Path, r.New.Version)
	}
	found := false
	for _, r := range mod.Require {
		if r.Path != "github.com/Loomux/server" {
			continue
		}
		found = true
		if err := checkPin(r.Version); err != "" {
			t.Errorf("go.mod requires github.com/Loomux/server %s: %s", r.Version, err)
		}
	}
	if !found {
		t.Error("go.mod doesn't require github.com/Loomux/server")
	}
}

var (
	// A release tag: vMAJOR.MINOR.PATCH with an optional SemVer pre-release.
	tagVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	// A pseudo-version, in any of its three forms (vX.0.0-T-H,
	// vX.Y.Z-pre.0.T-H, vX.Y.(Z+1)-0.T-H): golang.org/x/mod/module's
	// pattern, a 14-digit timestamp and a revision.
	pseudoVersion = regexp.MustCompile(`^v[0-9]+\.(0\.0-|[0-9]+\.[0-9]+-([^+]*\.)?0\.)[0-9]{14}-[A-Za-z0-9]+(\+[0-9A-Za-z-]+)?$`)
)

// checkPin says what is wrong with v as the server pin, or "" when it is a
// release tag.
func checkPin(v string) string {
	switch {
	case pseudoVersion.MatchString(v):
		return "a pseudo-version, not a release tag"
	case !tagVersion.MatchString(v):
		return "not a release tag (vX.Y.Z)"
	}
	return ""
}

func TestCheckPin(t *testing.T) {
	for v, ok := range map[string]bool{
		"v0.3.143":                                  true,
		"v1.0.0-rc.1":                               true,
		"v0.3.144-0.20261119101112-abcdef123456":    false,
		"v0.0.0-20261119101112-abcdef123456":        false,
		"v1.0.0-rc.1.0.20261119101112-abcdef123456": false,
		"v0.3.143+incompatible":                     false,
		"v0.3":                                      false,
		"0.3.143":                                   false,
		"main":                                      false,
		"":                                          false,
	} {
		if got := checkPin(v); (got == "") != ok {
			t.Errorf("checkPin(%q) = %q, want ok=%v", v, got, ok)
		}
	}
}

func TestClient(t *testing.T) {
	built := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/Loomux/tui", Version: v}}
	}
	for _, tc := range []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"stamped wins", "0.1.0", built("v0.1.0"), true, "0.1.0"},
		{"go install build", "", built("v0.1.2"), true, "v0.1.2"},
		{"devel build", "", built("(devel)"), true, "dev"},
		{"empty module version", "", built(""), true, "dev"},
		{"no build info", "", nil, false, "dev"},
	} {
		if got := client(tc.stamped, tc.bi, tc.ok); got != tc.want {
			t.Errorf("%s: client = %q, want %q", tc.name, got, tc.want)
		}
	}
}
