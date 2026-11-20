#!/bin/sh
# Release helpers (R3, LOOM-278), adapted from loomux/server's
# deploy/release.sh (LOOM-129). The client has its own SemVer line; see
# docs/release.md.
#
#   deploy/release.sh version <tag>            print the version a tag names
#   deploy/release.sh prerelease <version>     exit 0 if it's a pre-release
#   deploy/release.sh notes <version> [file]   print its CHANGELOG section
#   deploy/release.sh latest [rev|--all]       the highest vX.Y.Z tag reachable
#                                              from rev (HEAD), or of all tags
#   deploy/release.sh next <patch|minor> <version>
#                                              the version after it
#   deploy/release.sh reserve <commit> <patch|minor>
#                                              create the next version's tag on
#                                              a commit on main through the
#                                              GitHub API and print the version
#                                              (or the one it already has;
#                                              nothing before the first release)
#   deploy/release.sh check <tag>              the release workflow's gate: print
#                                              the version of a tag on main, and
#                                              refuse a milestone (X.Y.0, -pre)
#                                              without its CHANGELOG section
#
# A tag is "v" + a SemVer 2.0.0 version without build metadata. Every
# 0.y.z version and every version with a pre-release part (-alpha.1,
# -rc.2, ...) is a pre-release. A milestone's notes are its section of
# CHANGELOG.md ("## [X.Y.Z] - YYYY-MM-DD").
#
# Unlike the server's, nothing runs reserve on a merge: a person runs it
# with their own GitHub credentials, and the tag it creates starts the
# release workflow. RELEASE_BRANCH (default origin/main) is where a
# released commit must be.
set -eu

semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?$'
branch="${RELEASE_BRANCH:-origin/main}"

die() { echo "release: $*" >&2; exit 1; }

# on_branch <sha>: the commit is on the release branch.
on_branch() {
  git merge-base --is-ancestor "$1" "$branch" 2>/dev/null
}

case "${1:-}" in
version)
  tag="${2:-}"
  case "$tag" in v*) ;; *) die "tag '$tag' doesn't start with v" ;; esac
  v="${tag#v}"
  printf '%s\n' "$v" | grep -Eq "$semver" || die "tag '$tag' is not v + a SemVer 2.0.0 version (no build metadata)"
  printf '%s\n' "$v"
  ;;
prerelease)
  v="${2:-}"
  case "$v" in 0.* | *-*) exit 0 ;; *) exit 1 ;; esac
  ;;
notes)
  v="${2:-}"
  file="${3:-CHANGELOG.md}"
  [ -f "$file" ] || die "no $file"
  notes="$(mktemp)"
  trap 'rm -f "$notes"' EXIT
  # The section runs to the next version heading or the link definitions
  # ("[0.1.0]: https://...") that end the file.
  awk -v v="$v" '
    /^## \[/ { if (on) exit; on = (index($0, "## [" v "] - ") == 1); next }
    /^\[[^]]+\]: / { if (on) exit }
    on { print }
  ' "$file" > "$notes"
  grep -q '[^[:space:]]' "$notes" || die "$file has no non-empty section '## [$v] - <date>'"
  cat "$notes"
  ;;
latest)
  # Only plain X.Y.Z tags start the line: a -rc tag doesn't.
  if [ "${2:-}" = --all ]; then merged=""; else merged="--merged=${2:-HEAD}"; fi
  git tag ${merged} --list 'v*' |
    sed -n 's/^v\(\(0\|[1-9][0-9]*\)\.\(0\|[1-9][0-9]*\)\.\(0\|[1-9][0-9]*\)\)$/\1/p' |
    sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1
  ;;
next)
  kind="${2:-}"
  v="${3:-}"
  printf '%s\n' "$v" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
    die "next: '$v' is not X.Y.Z"
  major="${v%%.*}"; rest="${v#*.}"; minor="${rest%%.*}"; patch="${rest#*.}"
  case "$kind" in
  patch) echo "$major.$minor.$((patch + 1))" ;;
  minor) echo "$major.$((minor + 1)).0" ;;
  *) die "next: kind must be patch or minor" ;;
  esac
  ;;
reserve)
  # Every reserved commit gets its own version, even when two reserves
  # race: the tag is created through the API, which refuses one that
  # exists; the loser fetches the tags and takes the next. Needs gh
  # signed in with the right to create tags, and GITHUB_REPOSITORY
  # (owner/name).
  kind="${3:-patch}"
  git fetch -q --tags --force origin 2>/dev/null || true
  sha="$(git rev-parse --verify --quiet "${2:-}^{commit}")" || die "reserve: no commit '${2:-}'"
  # A tag can't be moved or deleted, so a commit off main would burn its
  # number for a release the workflow refuses.
  on_branch "$sha" || die "reserve: $sha is not on ${branch#origin/}"
  existing="$(git tag --points-at "$sha" --list 'v*' |
    sed -n 's/^v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' | head -n 1)"
  if [ -n "$existing" ]; then echo "$existing"; exit 0; fi
  [ -n "${GITHUB_REPOSITORY:-}" ] || die "reserve: set GITHUB_REPOSITORY (loomux/tui)"
  err="$(mktemp)"
  trap 'rm -f "$err"' EXIT
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    git fetch -q --tags --force origin 2>/dev/null || true
    latest="$("$0" latest --all)"
    if [ -z "$latest" ]; then
      [ "$kind" != minor ] || die "reserve: no release line yet; cut the first version by hand"
      exit 0
    fi
    # A commit older than the latest release is already in it: it
    # releases nothing rather than take a higher number than its
    # successor.
    at="$(git rev-list -n 1 "v$latest" 2>/dev/null || true)"
    if [ -n "$at" ] && [ "$at" != "$sha" ] && git merge-base --is-ancestor "$sha" "$at" 2>/dev/null; then
      exit 0
    fi
    v="$("$0" next "$kind" "$latest")"
    if gh api "repos/${GITHUB_REPOSITORY}/git/refs" -f ref="refs/tags/v$v" -f sha="$sha" >/dev/null 2>"$err"; then
      git tag "v$v" "$sha" 2>/dev/null || true
      echo "$v"
      exit 0
    fi
    grep -q 'Reference already exists' "$err" || die "reserve: creating tag v$v failed: $(cat "$err")"
    sleep 1
  done
  die "reserve: gave up after 10 tries"
  ;;
check)
  tag="${2:-}"
  v="$("$0" version "$tag")" || exit 1
  sha="$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}")" || die "check: no tag $tag"
  on_branch "$sha" || die "check: $tag is not on ${branch#origin/}"
  # A milestone says what it is: its notes are its CHANGELOG section, as
  # the tagged commit has it.
  case "$v" in
  *-* | *.*.0)
    changelog="$(mktemp)"
    trap 'rm -f "$changelog"' EXIT
    git show "$sha:CHANGELOG.md" > "$changelog" 2>/dev/null || true
    "$0" notes "$v" "$changelog" >/dev/null 2>&1 ||
      die "check: $tag is a milestone, and CHANGELOG.md at it has no '## [$v] - <date>' section (deploy/changelog.sh release)"
    ;;
  esac
  echo "$v"
  ;;
*)
  die "usage: $0 version <tag> | prerelease <version> | notes <version> [file] | latest [rev|--all] | next <patch|minor> <version> | reserve <commit> <patch|minor> | check <tag>"
  ;;
esac
