package version

import (
	"os"
	"os/exec"
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
