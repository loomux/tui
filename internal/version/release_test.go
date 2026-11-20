package version_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// deploy/release.sh is what the release workflow trusts to read a tag,
// adapted from loomux/server's (LOOM-129): exercise it here, since a
// release tag is pushed only once and can't be moved or deleted.

func script(name string) string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(wd, "..", "..", "deploy", name)
}

func release(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	out, err := exec.Command("sh", append([]string{script("release.sh")}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func TestReleaseVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"v0.1.0": "0.1.0", "v1.0.0-rc.1": "1.0.0-rc.1", "v1.2.3-alpha.1.x-y": "1.2.3-alpha.1.x-y", "v10.20.30": "10.20.30",
	} {
		if got, ok := release(t, "version", tag); !ok || got != want {
			t.Errorf("version %s = %q, %v; want %q", tag, got, ok, want)
		}
	}
	for _, tag := range []string{"0.1.0", "v1.0", "v01.0.0", "v1.0.0+build.1", "v1.0.0-01", "v1.0.0-", "vx.y.z", "v1.0.0 ; rm"} {
		if got, ok := release(t, "version", tag); ok {
			t.Errorf("version %s accepted as %q", tag, got)
		}
	}
}

func TestReleasePrerelease(t *testing.T) {
	for v, want := range map[string]bool{"0.1.0": true, "0.9.3": true, "1.0.0-rc.1": true, "1.0.0": false, "2.3.4": false} {
		if _, ok := release(t, "prerelease", v); ok != want {
			t.Errorf("prerelease %s = %v, want %v", v, ok, want)
		}
	}
}

const testChangelog = `# Changelog

## [Unreleased]

- next thing

## [0.2.0] - 2026-10-06

### Added
- second

## [0.1.0] - 2026-10-05

### Added
- first

[0.2.0]: https://example.com
`

func TestReleaseNotes(t *testing.T) {
	changelog := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(changelog, []byte(testChangelog), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := release(t, "notes", "0.2.0", changelog); !ok || got != "### Added\n- second" {
		t.Errorf("notes 0.2.0 = %q, %v", got, ok)
	}
	if got, ok := release(t, "notes", "0.1.0", changelog); !ok || got != "### Added\n- first" {
		t.Errorf("notes 0.1.0 = %q, %v", got, ok)
	}
	if _, ok := release(t, "notes", "0.3.0", changelog); ok {
		t.Error("a version without a CHANGELOG section was accepted")
	}
	if _, ok := release(t, "notes", "0.1", changelog); ok {
		t.Error("a prefix of a version matched its section")
	}
}

func TestReleaseNext(t *testing.T) {
	for args, want := range map[[2]string]string{
		{"patch", "0.1.0"}: "0.1.1", {"patch", "0.1.9"}: "0.1.10", {"minor", "0.1.7"}: "0.2.0", {"minor", "1.9.3"}: "1.10.0",
	} {
		if got, ok := release(t, "next", args[0], args[1]); !ok || got != want {
			t.Errorf("next %v = %q, %v; want %q", args, got, ok, want)
		}
	}
	for _, args := range [][2]string{{"patch", "0.1"}, {"major", "0.1.0"}, {"patch", "1.0.0-rc.1"}} {
		if got, ok := release(t, "next", args[0], args[1]); ok {
			t.Errorf("next %v accepted as %q", args, got)
		}
	}
}

// repo is a scratch git repository for the scripts to run in.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.email=t@example.org", "-c", "user.name=t", "-c", "tag.gpgSign=false", "-c", "commit.gpgSign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// run runs release.sh in the repository, with gh (if given) as a fake
// on PATH and extra environment.
func (r *repo) run(gh string, env []string, args ...string) (string, bool) {
	r.t.Helper()
	cmd := exec.Command("sh", append([]string{script("release.sh")}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = os.Environ()
	if gh != "" {
		bin := r.t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
			r.t.Fatal(err)
		}
		cmd.Env = append(cmd.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// The line continues from the highest plain version reachable from the
// commit: not a -rc, not a tag on another branch, and compared as
// numbers (0.1.10 > 0.1.9).
func TestReleaseLatest(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "a")
	if got, ok := r.run("", nil, "latest"); !ok || got != "" {
		t.Errorf("latest with no tags = %q, %v", got, ok)
	}
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "b")
	r.git("tag", "v0.1.9")
	r.git("commit", "-q", "--allow-empty", "-m", "c")
	r.git("tag", "v0.1.10")
	r.git("tag", "v1.0.0-rc.1")
	r.git("tag", "not-a-version")
	r.git("checkout", "-q", "-b", "side", "HEAD~2")
	r.git("commit", "-q", "--allow-empty", "-m", "side")
	r.git("tag", "v0.5.0")
	r.git("checkout", "-q", "main")
	if got, ok := r.run("", nil, "latest"); !ok || got != "0.1.10" {
		t.Errorf("latest = %q, %v; want 0.1.10", got, ok)
	}
}

// reserveEnv names the repository and, since these repositories have no
// remote, the branch a reserved commit must be on.
var reserveEnv = []string{"GITHUB_REPOSITORY=loomux/x", "RELEASE_BRANCH=main"}

// Two merges' runs racing for the next version both get one. The fake gh
// refuses the first tag as if the other run had just created it (and
// that tag appears, as a fetch would bring it); reserve takes the next.
func TestReleaseReserve(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "other merge")
	other := r.git("rev-parse", "HEAD")
	r.git("commit", "-q", "--allow-empty", "-m", "this merge")
	sha := r.git("rev-parse", "HEAD")

	created := filepath.Join(t.TempDir(), "created")
	gh := `#!/bin/sh
case "$*" in
*refs/tags/v0.1.1*) git -C "` + r.dir + `" tag v0.1.1 ` + other + `; echo 'gh: Reference already exists (HTTP 422)' >&2; exit 1 ;;
*) echo "$*" >> "` + created + `"; exit 0 ;;
esac
`
	// A ref, not only a sha, names the commit: it is resolved first.
	if got, ok := r.run(gh, reserveEnv, "reserve", "main", "patch"); !ok || got != "0.1.2" {
		t.Fatalf("reserve = %q, %v; want 0.1.2 after losing 0.1.1", got, ok)
	}
	b, _ := os.ReadFile(created)
	if !strings.Contains(string(b), "repos/loomux/x/git/refs") || !strings.Contains(string(b), "ref=refs/tags/v0.1.2") || !strings.Contains(string(b), "sha="+sha) {
		t.Errorf("gh calls = %q", b)
	}
	// Asked again for the same commit, it keeps its version.
	if got, ok := r.run(gh, reserveEnv, "reserve", sha, "patch"); !ok || got != "0.1.2" {
		t.Errorf("re-run reserve = %q, %v; want 0.1.2 again", got, ok)
	}
}

// A merge's commit that also has a milestone tag pushed after its run
// reserved keeps its own patch when that run is re-run: the lowest of its
// versions, by number (0.9.5 before 0.10.0, which sorts first as text).
func TestReleaseReserveKeepsTheMergesVersion(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.git("tag", "v0.9.4")
	r.git("commit", "-q", "--allow-empty", "-m", "merge")
	r.git("tag", "v0.9.5")
	r.git("tag", "v0.10.0")
	r.git("tag", "v0.10.0-rc.1") // not a version reserve gives
	if got, ok := r.run("#!/bin/sh\necho called >&2; exit 1\n", reserveEnv, "reserve", "main", "patch"); !ok || got != "0.9.5" {
		t.Errorf("reserve = %q, %v; want 0.9.5, the merge's own version", got, ok)
	}
}

// Two reserves of the same commit at once give it one version: the one
// that loses the race finds the commit tagged and prints that, rather
// than tag it again with the next number.
func TestReleaseReserveSameCommitRace(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "merge")
	sha := r.git("rev-parse", "HEAD")
	created := filepath.Join(t.TempDir(), "created")
	gh := `#!/bin/sh
case "$*" in
*refs/tags/v0.1.1*) git -C "` + r.dir + `" tag v0.1.1 ` + sha + `; echo 'gh: Reference already exists (HTTP 422)' >&2; exit 1 ;;
*) echo "$*" >> "` + created + `"; exit 0 ;;
esac
`
	if got, ok := r.run(gh, reserveEnv, "reserve", sha, "patch"); !ok || got != "0.1.1" {
		t.Errorf("reserve = %q, %v; want 0.1.1, the version the other reserve gave it", got, ok)
	}
	if b, _ := os.ReadFile(created); len(b) > 0 {
		t.Errorf("reserve created another tag: %q", b)
	}
}

// A reserve that can't fetch stops: main and the tags it would go by may
// be stale.
func TestReleaseReserveFetchFails(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "merge")
	r.git("remote", "add", "origin", filepath.Join(t.TempDir(), "gone"))
	got, ok := r.run("#!/bin/sh\necho called >&2; exit 1\n", reserveEnv, "reserve", "main", "patch")
	if ok || !strings.Contains(got, "fetch") || strings.Contains(got, "called") {
		t.Errorf("reserve with a failing fetch = %q, %v", got, ok)
	}
}

// A patch's notes: main's first-parent commits since the previous release,
// newest first, so a patch that covers several merged pull requests lists
// each of them.
func TestReleaseLog(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "First (#1)")
	if got, ok := r.run("", nil, "log", "main"); !ok || got != "- First (#1)" {
		t.Errorf("log with no release before = %q, %v", got, ok)
	}
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "A (#2)")
	r.git("tag", "v0.2.0-rc.1") // not a release the line goes by
	r.git("checkout", "-q", "-b", "side")
	r.git("commit", "-q", "--allow-empty", "-m", "side work")
	r.git("checkout", "-q", "main")
	r.git("-c", "core.editor=true", "merge", "-q", "--no-ff", "-m", "B (#3)", "side")
	r.git("tag", "v0.1.1")
	if got, ok := r.run("", nil, "log", "v0.1.1"); !ok || got != "- B (#3)\n- A (#2)" {
		t.Errorf("log v0.1.1 = %q, %v", got, ok)
	}
	if _, ok := r.run("", nil, "log", "nope"); ok {
		t.Error("log of an unknown commit succeeded")
	}
}

// When a newer merge was released first, the older merge (an ancestor of
// it) releases nothing rather than take a higher version than its
// successor.
func TestReleaseReserveOlderMergeAfterNewer(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.git("tag", "v0.1.0")
	r.git("commit", "-q", "--allow-empty", "-m", "older merge")
	older := r.git("rev-parse", "HEAD")
	r.git("commit", "-q", "--allow-empty", "-m", "newer merge")
	r.git("tag", "v0.1.1")
	got, ok := r.run("#!/bin/sh\necho called >&2; exit 1\n", reserveEnv, "reserve", older, "patch")
	if !ok || got != "" {
		t.Errorf("reserve for the older merge = %q, %v; want nothing released", got, ok)
	}
}

// reserve tags only a commit on the release branch: a tag can't be
// moved or deleted, so a wrong one would burn its number for nothing.
// It needs GITHUB_REPOSITORY, and before the first release (cut by hand)
// it reserves nothing.
func TestReleaseReserveRefusals(t *testing.T) {
	r := newRepo(t)
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	none := "#!/bin/sh\necho called >&2; exit 1\n"
	if got, ok := r.run(none, reserveEnv, "reserve", "main", "patch"); !ok || got != "" {
		t.Errorf("reserve before the first release = %q, %v; want nothing", got, ok)
	}
	if got, ok := r.run(none, reserveEnv, "reserve", "main", "minor"); ok {
		t.Errorf("reserve minor before the first release = %q, want an error", got)
	}
	r.git("tag", "v0.1.0")
	r.git("checkout", "-q", "-b", "topic")
	r.git("commit", "-q", "--allow-empty", "-m", "unmerged")
	got, ok := r.run(none, reserveEnv, "reserve", "topic", "patch")
	if ok || !strings.Contains(got, "not on main") || strings.Contains(got, "called") {
		t.Errorf("reserve of a commit not on main = %q, %v", got, ok)
	}
	r.git("checkout", "-q", "main")
	r.git("commit", "-q", "--allow-empty", "-m", "merged")
	got, ok = r.run(none, []string{"GITHUB_REPOSITORY=", "RELEASE_BRANCH=main"}, "reserve", "main", "patch")
	if ok || !strings.Contains(got, "GITHUB_REPOSITORY") || strings.Contains(got, "called") {
		t.Errorf("reserve without GITHUB_REPOSITORY = %q, %v", got, ok)
	}
	got, ok = r.run(none, reserveEnv, "reserve", "no-such-ref", "patch")
	if ok || strings.Contains(got, "called") {
		t.Errorf("reserve of an unknown ref = %q, %v", got, ok)
	}
}

// check is the release workflow's gate for a pushed tag: v + SemVer, on
// the release branch, and its CHANGELOG.md section at the tagged commit.
// A pushed tag is a milestone; a patch is a merge's, which reserve tags.
// It prints the version.
func TestReleaseCheck(t *testing.T) {
	r := newRepo(t)
	if err := os.WriteFile(filepath.Join(r.dir, "CHANGELOG.md"), []byte(testChangelog), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("add", "CHANGELOG.md")
	r.git("commit", "-q", "-m", "base")
	r.git("tag", "v0.1.0")
	r.git("tag", "v0.2.0")
	r.git("tag", "v0.2.1")
	r.git("tag", "v0.3.0")
	r.git("tag", "v1.0.0-rc.1")
	r.git("tag", "v0.2")
	r.git("checkout", "-q", "-b", "topic")
	r.git("commit", "-q", "--allow-empty", "-m", "unmerged")
	r.git("tag", "v0.2.2")
	r.git("checkout", "-q", "main")
	env := []string{"RELEASE_BRANCH=main"}
	for tag, want := range map[string]string{"v0.1.0": "0.1.0", "v0.2.0": "0.2.0"} {
		if got, ok := r.run("", env, "check", tag); !ok || got != want {
			t.Errorf("check %s = %q, %v; want %q", tag, got, ok, want)
		}
	}
	for tag, why := range map[string]string{
		"v0.2.1":      "CHANGELOG", // a patch pushed by hand
		"v0.3.0":      "CHANGELOG", // a milestone without its section
		"v1.0.0-rc.1": "CHANGELOG",
		"v0.2.2":      "not on main",
		"v0.2":        "SemVer",
		"v9.9.9":      "no tag",
	} {
		if got, ok := r.run("", env, "check", tag); ok || !strings.Contains(got, why) {
			t.Errorf("check %s = %q, %v; want an error saying %q", tag, got, ok, why)
		}
	}
}

// deploy/build.sh builds the release binaries: CGO off, for linux and
// darwin on amd64 and arm64, the version stamped with -ldflags -X, and a
// SHA256SUMS file covering them.
func TestBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds four binaries")
	}
	out := t.TempDir()
	cmd := exec.Command("sh", script("build.sh"), "0.0.1-test.1", out)
	cmd.Dir = filepath.Join("..", "..")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v: %s", err, b)
	}
	targets := []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"}
	sums := map[string]string{}
	f, err := os.Open(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			t.Fatalf("SHA256SUMS line %q isn't sha256sum's format", sc.Text())
		}
		sums[name] = sum
	}
	if len(sums) != len(targets) {
		t.Errorf("SHA256SUMS has %d entries, want %d: %v", len(sums), len(targets), sums)
	}
	for _, target := range targets {
		name := "loomux-0.0.1-test.1-" + target
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		h := sha256.Sum256(data)
		if got := hex.EncodeToString(h[:]); sums[name] != got {
			t.Errorf("SHA256SUMS says %s is %q, it is %q", name, sums[name], got)
		}
	}
	// The binary for this machine reports the stamped version.
	host := filepath.Join(out, "loomux-0.0.1-test.1-"+runtime.GOOS+"-"+runtime.GOARCH)
	if _, err := os.Stat(host); err == nil {
		b, err := exec.Command(host, "version").Output()
		if err != nil || !strings.HasPrefix(string(b), "loomux 0.0.1-test.1\n") {
			t.Errorf("%s version = %q, %v", host, b, err)
		}
	}
	// No other files: the release uploads the directory.
	entries, _ := os.ReadDir(out)
	if len(entries) != len(targets)+1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("output has %v", names)
	}
}

// A version that isn't SemVer is refused before anything is built.
func TestBuildRefusesBadVersion(t *testing.T) {
	out := t.TempDir()
	for _, v := range []string{"", "v0.1.0", "0.1", "0.1.0+meta", "0.1.0;rm"} {
		cmd := exec.Command("sh", script("build.sh"), v, out)
		cmd.Dir = filepath.Join("..", "..")
		if b, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("build.sh %q succeeded: %s", v, b)
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("a refused build wrote %d files", len(entries))
	}
}

// topLevel returns a top-level key's block in a workflow written in block
// style: its own line and every line after it up to the next one that
// starts in column 0, without blank lines, and how many times the key
// is at the top level.
func topLevel(data, key string) (string, int) {
	var block []string
	n, in := 0, false
	for _, line := range strings.Split(data, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case line[0] != ' ' && line[0] != '\t':
			in = strings.HasPrefix(line, key+":")
			if in {
				n++
			}
		}
		if in {
			block = append(block, line)
		}
	}
	return strings.Join(block, "\n"), n
}

// A blank line doesn't end a YAML mapping: a key indented after one
// still belongs to the block, so it can't slip a trigger past the pin.
func TestTopLevel(t *testing.T) {
	const wf = "name: x\non:\n  push:\n    branches: [main]\n\n  pull_request:\n\npermissions: {}\njobs:\n  a:\n    on: no\n"
	if got, n := topLevel(wf, "on"); n != 1 || got != "on:\n  push:\n    branches: [main]\n  pull_request:" {
		t.Errorf("topLevel(on) = %q, %d", got, n)
	}
	if got, n := topLevel(wf, "concurrency"); n != 0 || got != "" {
		t.Errorf("topLevel(concurrency) = %q, %d", got, n)
	}
}

// Only a merge to main (the next patch) and a pushed v* tag (a
// milestone) publish: release.yml runs on nothing else, not on a pull
// request or another branch, and no other workflow (pull requests,
// branch pushes, the nightly run) can write to the repository, mint an
// OIDC token or attest. release.yml has no run-level concurrency group:
// a group keeps one pending run and cancels the rest, which drops a
// merge's release (loomux/server, LOOM-129).
func TestOnlyMainAndTagsPublish(t *testing.T) {
	dir := filepath.Join("..", "..", ".github", "workflows")
	data, err := os.ReadFile(filepath.Join(dir, "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "on:\n  push:\n    branches: [main]\n    tags: [\"v*\"]"
	if on, n := topLevel(string(data), "on"); on != want || n != 1 {
		t.Errorf("release.yml must run only on a push to main or of a v* tag:\n%s\nnot:\n%s", want, on)
	}
	if c, n := topLevel(string(data), "concurrency"); n != 0 {
		t.Errorf("release.yml has a run-level concurrency group, which cancels pending merges' runs:\n%s", c)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "release.yml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, perm := range []string{"contents: write", "id-token: write", "attestations: write", "packages: write", "pull-requests: write", "write-all"} {
			if strings.Contains(string(b), perm) {
				t.Errorf("%s has %q: only release.yml, on main or a tag, publishes", e.Name(), perm)
			}
		}
	}
}

// Milestones, dry run: this repository's CHANGELOG.md takes the
// fragments in changes/ (deploy/changelog.sh release), and the section
// that makes is what release.sh reads as the notes. It folds two
// versions no real milestone will have, so the test passes on the file as
// every milestone leaves it, with its own sections in it.
func TestChangelogRelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	changelog := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(changelog, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"998.0.0", "999.0.0"} {
		changes := filepath.Join(dir, "changes-"+v)
		if err := os.Mkdir(changes, 0o755); err != nil {
			t.Fatal(err)
		}
		fragment := "### Added\n\n- A thing for " + v + " (LOOM-1).\n"
		if err := os.WriteFile(filepath.Join(changes, "loom-1-thing.md"), []byte(fragment), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", script("changelog.sh"), "release", v, "2026-11-20")
		cmd.Env = append(os.Environ(), "CHANGES_DIR="+changes, "CHANGELOG="+changelog)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("changelog.sh release %s: %v: %s", v, err, b)
		}
		got, ok := release(t, "notes", v, changelog)
		if want := "### Added\n\n- A thing for " + v + " (LOOM-1)."; !ok || got != want {
			t.Errorf("notes %s = %q, %v; want %q", v, got, ok, want)
		}
	}
	b, _ := os.ReadFile(changelog)
	for _, want := range []string{
		"## [Unreleased]",
		"## [999.0.0] - 2026-11-20",
		"## [998.0.0] - 2026-11-20",
		"[Unreleased]: https://github.com/loomux/tui/compare/v999.0.0...HEAD",
		"[999.0.0]: https://github.com/loomux/tui/releases/tag/v999.0.0",
		"[998.0.0]: https://github.com/loomux/tui/releases/tag/v998.0.0",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("CHANGELOG.md after two milestones lacks %q:\n%s", want, b)
		}
	}
}
