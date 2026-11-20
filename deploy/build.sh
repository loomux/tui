#!/bin/sh
# The release binaries (R3, LOOM-278; the design's "Packaging"):
#
#   deploy/build.sh VERSION OUTDIR
#
# builds cmd/loomux with CGO off for linux and darwin on amd64 and arm64,
# with VERSION stamped by -ldflags -X, as OUTDIR/loomux-VERSION-OS-ARCH,
# and writes OUTDIR/SHA256SUMS for them. OUTDIR must be empty or new: the
# release uploads all of it. Run it from the repository root. The release
# workflow runs it on a tag; TestBuild runs it on every pull request.
set -eu

die() { echo "build: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: $0 VERSION OUTDIR"
version="$1"
out="$2"
sh "$(dirname "$0")/release.sh" version "v$version" >/dev/null 2>&1 ||
  die "'$version' isn't a SemVer 2.0.0 version (no v, no build metadata)"
[ -f cmd/loomux/main.go ] || die "run it from the repository root"
mkdir -p "$out"
[ -z "$(ls -A "$out")" ] || die "$out isn't empty"

pkg=github.com/Loomux/tui/internal/version
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $pkg.Version=$version" \
    -o "$out/loomux-$version-$os-$arch" ./cmd/loomux
done

# The binary for this machine, when there is one, reports the version.
host="$out/loomux-$version-$(go env GOOS)-$(go env GOARCH)"
if [ -f "$host" ]; then
  got="$("$host" version | head -n 1)"
  [ "$got" = "loomux $version" ] || die "$host version says '$got', not 'loomux $version'"
fi

if command -v sha256sum >/dev/null 2>&1; then sum="sha256sum"; else sum="shasum -a 256"; fi
(cd "$out" && $sum loomux-* > SHA256SUMS)
