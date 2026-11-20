# Releases

`loomux` has its own [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html)
line, apart from the server's and the web client's. The server doesn't
pin or report it, and compatibility is by API version and feature
detection. The design is in `loomux/server`, in
`docs/design/tui-client-design.md` under "Releases and versioning" and
"Packaging". These scripts are adapted from that repository's
`deploy/release.sh` and `deploy/changelog.sh`.

## What a release is

- A `vX.Y.Z` tag on `main`. `v*` tags can't be moved or deleted, so a
  failed release burns its number and versions are never reused.
- The release workflow (`.github/workflows/release.yml`) builds `loomux`
  with `CGO_ENABLED=0` for Linux and macOS on amd64 and arm64. The
  version is stamped with `-ldflags -X`, so `loomux version` prints it.
  The binaries go on a GitHub release with `SHA256SUMS`, and each binary
  gets a build provenance attestation.
- Every 0.y.z and `-pre` version is a GitHub **pre-release**, never marked
  "latest". 0.x lasts until the user's go for 1.0.0.

**Only a pushed tag publishes.** The release workflow runs on nothing
but a `v*` tag push. Pull requests and branch pushes run `ci.yml`, which
can't write to the repository. Its `TestBuild` runs the same build
script, and `TestOnlyATagPublishes` holds both of these rules.

## Cutting a release

A **patch** releases what's merged on `main`:

```sh
GITHUB_REPOSITORY=loomux/tui deploy/release.sh reserve origin/main patch
```

`reserve` takes the highest `vX.Y.Z` tag plus one and creates that tag
on the commit through the GitHub API, with your `gh` credentials. It
prints the version, and the new tag starts the release workflow. It
refuses a commit that isn't on `main`. Asked again for a commit that
already has a version, it prints that version and creates nothing. When
two reserves race, the second takes the next number.

A **milestone** is each MINOR (0.2.0, 0.3.0…), the first version
(0.1.0), and later an `-rc` or 1.0.0:

1. A pull request runs
   `deploy/changelog.sh release X.Y.0 YYYY-MM-DD`. That folds the
   fragments in `changes/` into a `## [X.Y.0]` section of `CHANGELOG.md`
   and deletes them. Commit both.
2. After it merges, tag its merge commit by hand:
   `git tag vX.Y.0 <sha> && git push origin vX.Y.0`.

## What the workflow checks

1. **check:** the tag is `v` + SemVer without build metadata, and it is on
   `main`. A milestone (X.Y.0, or a version with a `-pre` part) needs its
   `CHANGELOG.md` section at the tagged commit (`deploy/release.sh
   check`).
2. **build:** `go vet` and `go test -race` on the tagged commit. Then
   `deploy/build.sh VERSION dist` builds the four binaries, checks that
   the one for the runner reports the version, and writes `SHA256SUMS`.
   It ends by attesting each binary.
3. **release:** the files are checked against `SHA256SUMS`. The notes
   are the milestone's `CHANGELOG.md` section, or for a patch the pull
   request merged as the tagged commit, plus the server pin. The
   release is created as a draft with every file and then published. A
   re-run after a failure replaces a draft left behind and leaves a
   published release alone.

## Checking a download

```sh
sha256sum -c --ignore-missing SHA256SUMS
gh attestation verify loomux-X.Y.Z-linux-amd64 -R loomux/tui
chmod +x loomux-X.Y.Z-linux-amd64
```

`docs/install.md` (T11) will cover installing in full.
`go install github.com/Loomux/tui/cmd/loomux@vX.Y.Z` works too. That
build reports the module version from its build info.
