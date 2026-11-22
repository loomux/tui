package version_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Loomux/tui/internal/version"
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// docs/install.md installs what a release publishes: the files
// deploy/build.sh names, for each platform it builds, the SHA256SUMS it
// writes, the attestation release.yml makes, and the module go install
// fetches. If one of those changes, the doc changes with it.
func TestInstallDocMatchesTheRelease(t *testing.T) {
	doc := repoFile(t, "docs/install.md")
	build := repoFile(t, "deploy/build.sh")
	wf := repoFile(t, ".github/workflows/release.yml")

	// The file names: build.sh's, and the doc's script builds the same.
	if !strings.Contains(build, `-o "$out/loomux-$version-$os-$arch"`) {
		t.Fatal("build.sh no longer names the binaries loomux-VERSION-OS-ARCH: update docs/install.md and this test")
	}
	if !strings.Contains(doc, `file="loomux-$VERSION-$os-$arch"`) {
		t.Error("docs/install.md doesn't build the file's name as build.sh does")
	}
	m := regexp.MustCompile(`(?m)^for target in ([a-z0-9/ ]+); do$`).FindStringSubmatch(build)
	if m == nil {
		t.Fatal("build.sh's targets not found")
	}
	for _, target := range strings.Fields(m[1]) {
		os, arch, _ := strings.Cut(target, "/")
		if name := "`loomux-X.Y.Z-" + os + "-" + arch + "`"; !strings.Contains(doc, name) {
			t.Errorf("docs/install.md doesn't list %s, which build.sh builds", name)
		}
	}
	for _, name := range regexp.MustCompile("`loomux-X\\.Y\\.Z-([a-z0-9]+)-([a-z0-9]+)`").FindAllStringSubmatch(doc, -1) {
		if !strings.Contains(m[1], name[1]+"/"+name[2]) {
			t.Errorf("docs/install.md lists %s, which build.sh doesn't build", name[0])
		}
	}

	// The checks: SHA256SUMS, and the attestation of each binary by the
	// release workflow.
	if !strings.Contains(build, "> SHA256SUMS") {
		t.Fatal("build.sh no longer writes SHA256SUMS")
	}
	for _, cmd := range []string{
		"sha256sum -c --ignore-missing SHA256SUMS",
		"shasum -a 256 -c --ignore-missing SHA256SUMS",
		`gh attestation verify "$file" -R loomux/tui --signer-workflow loomux/tui/.github/workflows/release.yml`,
	} {
		if !strings.Contains(doc, cmd) {
			t.Errorf("docs/install.md lacks %q", cmd)
		}
	}
	if !strings.Contains(wf, "subject-path: dist/loomux-*") {
		t.Error("release.yml no longer attests each binary (subject-path: dist/loomux-*), which docs/install.md checks")
	}

	// The downloads name the release's tag, v + the version.
	for _, cmd := range []string{
		`gh release download "v$VERSION" -R loomux/tui -p "$file" -p SHA256SUMS`,
		`base="https://github.com/loomux/tui/releases/download/v$VERSION"`,
	} {
		if !strings.Contains(doc, cmd) {
			t.Errorf("docs/install.md lacks %q", cmd)
		}
	}

	// go install: go.mod's module path, which is case-sensitive.
	mod := regexp.MustCompile(`(?m)^module (\S+)$`).FindStringSubmatch(repoFile(t, "go.mod"))
	if mod == nil {
		t.Fatal("go.mod has no module line")
	}
	if want := "go install " + mod[1] + "/cmd/loomux@vX.Y.Z"; !strings.Contains(doc, want) {
		t.Errorf("docs/install.md lacks %q", want)
	}
}

// The README and docs/release.md point at docs/install.md.
func TestInstallDocLinked(t *testing.T) {
	for _, f := range []struct{ name, link string }{
		{"README.md", "(docs/install.md)"},
		{"docs/release.md", "(install.md)"},
	} {
		if !strings.Contains(repoFile(t, f.name), f.link) {
			t.Errorf("%s doesn't link docs/install.md", f.name)
		}
	}
}

// The README's table of server versions has the server pin for main:
// the release built from it needs that server or a later one. A pin
// bump updates the row.
func TestREADMEServerTable(t *testing.T) {
	readme := repoFile(t, "README.md")
	want := "| `main` (the next release) | " + strings.TrimPrefix(version.ServerPin, "v") + " or later |"
	if !strings.Contains(readme, want) {
		t.Errorf("README.md's server table lacks the row for main:\n%s", want)
	}
}
