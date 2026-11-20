# Releases

`loomux` has its own [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html)
line, apart from the server's and the web client's. The server doesn't
pin or report it, and compatibility is by API version and feature
detection. The design is in `loomux/server`, in
`docs/design/tui-client-design.md` under "Releases and versioning" and
"Packaging". The workflow and scripts follow that repository's
`.github/workflows/image.yml`, `deploy/release.sh` and
`deploy/changelog.sh`, and its `docs/release/versioning.md`.

## What a release is

- **Every merge to `main` is a patch pre-release** (0.1.1, 0.1.2…),
  once the line has its first version, 0.1.0. One merged pull request is
  one release. Nobody has to do anything.
- **A milestone is cut by hand:** the first version (0.1.0), each MINOR
  (0.2.0, 0.3.0…), and later an `-rc` or 1.0.0. A person pushes its tag,
  and its notes are its `CHANGELOG.md` section.
- Either way, the release is a `vX.Y.Z` tag on `main`. `v*` tags can't
  be moved or deleted (a ruleset), so a version is never reused.
- The release workflow (`.github/workflows/release.yml`) builds `loomux`
  with `CGO_ENABLED=0` for Linux and macOS on amd64 and arm64. The
  version is stamped with `-ldflags -X`, so `loomux version` prints it.
  The binaries go on a GitHub release with `SHA256SUMS`, and each binary
  gets a build provenance attestation.
- Every 0.y.z and `-pre` version is a GitHub **pre-release**, never marked
  "latest". 0.x lasts until the user's go for 1.0.0.

**Only a merge to `main` or a pushed `v*` tag publishes.** The release
workflow runs on nothing else. Pull requests and other branches run
`ci.yml`, which can't write to the repository. Its `TestBuild` runs the
same build script, and `TestOnlyMainAndTagsPublish` holds these rules.

## How a merge gets its version

The **version** job of the merge's run runs `deploy/release.sh reserve`
on the merge commit:

- It takes the highest `vX.Y.Z` tag in the repository plus one, and
  creates that tag on the commit through the GitHub API with the run's
  token. The API refuses a tag that already exists.
- **Racing merges each get their own version.** The run that loses the
  race fetches the tags and takes the next number. There's no
  concurrency group for the run, so no merge's run is queued or
  cancelled: a group keeps one pending run and cancels the others, which
  dropped a merge's release in the server.
- **One version per commit.** A re-run of a commit that already has a
  version keeps it, and builds and releases that version again.
- **An older merge after a newer one.** If a newer merge's run reserved
  first, an older merge it contains releases nothing of its own. Its
  changes ship in the newer version, whose notes list both.
- **A failed run burns its number** unless it is re-run, so re-run it.
  If it isn't, the next merge takes the next number and the line has a
  gap (SemVer allows gaps). The failed version's merges ship in the next
  release, and its notes list them.
- Before the first milestone, a merge reserves nothing and releases
  nothing.

The **build** and **release** jobs then run in the same workflow run. A
tag created with the run's `GITHUB_TOKEN` starts no other workflow, so
the tag a merge reserves doesn't start a second run.

## Cutting a milestone

1. A pull request runs
   `deploy/changelog.sh release X.Y.0 YYYY-MM-DD`. That folds the
   fragments in `changes/` into a `## [X.Y.0]` section of `CHANGELOG.md`
   and deletes them. Edit the section into prose and commit both.
2. It merges. Like every merge, it gets its own patch version first
   (except before 0.1.0).
3. Tag its merge commit and push the tag:
   `git tag vX.Y.0 <sha> && git push origin vX.Y.0`. Its run builds and
   releases X.Y.0 with the section as its notes. The next merge
   continues from it (X.Y.1).

If the tag is pushed before the merge's own run reserved a version, that
run takes the milestone as the commit's version and releases it too,
held to the same check. The release job runs one version at a time, so
the second run finds it published.

## What the workflow checks

1. **version:**
   - A merge: `reserve`, as above.
   - A pushed tag: `deploy/release.sh check`. The tag is `v` + SemVer
     without build metadata, and it is on `main`. It needs its
     `CHANGELOG.md` section at the tagged commit: a pushed tag is a
     milestone, and a patch is a merge's. Without a version, nothing
     else runs.
2. **build:** `go vet` and `go test -race` on the commit. Then
   `deploy/build.sh VERSION dist` builds the four binaries, checks that
   the one for the runner reports the version, and writes `SHA256SUMS`.
   It ends by attesting each binary.
3. **release:**
   - The files are checked against `SHA256SUMS`.
   - The notes are a milestone's `CHANGELOG.md` section. For a patch,
     they are the commits merged to `main` since the previous published
     release instead (`deploy/release.sh log`), one pull request each:
     usually just the merge. Either way they end with the server pin.
   - The release is created as a draft with every file, then published.
     A re-run after a failure replaces a draft left behind and leaves a
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
