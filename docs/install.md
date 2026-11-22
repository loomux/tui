# Installing loomux

`loomux` is a single binary for Linux and macOS, on x86-64 and ARM64.
Windows isn't supported. Each release is a GitHub release of
`loomux/tui` with four binaries, a `SHA256SUMS` file and a build
provenance attestation for each binary ([`release.md`](release.md) says
how they are made). You can also build it with `go install`
([below](#with-go-install)).

Check which Loomux server the release needs first: the README's
[server versions](../README.md#server-versions).

## 1. Choose the version and the file

The releases are at <https://github.com/loomux/tui/releases>, and
`gh release list -R loomux/tui` lists them. Every 0.x release is a
GitHub pre-release, never marked "latest". So name the version: a
`releases/latest` link, or `gh release download` without a tag, finds
nothing.

| System | File |
|---|---|
| Linux, x86-64 | `loomux-X.Y.Z-linux-amd64` |
| Linux, ARM64 | `loomux-X.Y.Z-linux-arm64` |
| macOS, Intel | `loomux-X.Y.Z-darwin-amd64` |
| macOS, Apple silicon | `loomux-X.Y.Z-darwin-arm64` |

The commands below use these shell variables. Set `VERSION` to the
release you want, without the `v`:

```sh
VERSION=0.1.0
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "no loomux build for $(uname -m)" ;;
esac
file="loomux-$VERSION-$os-$arch"
```

## 2. Download it

Download the binary and `SHA256SUMS` from the release into an empty
directory, with the GitHub CLI:

```sh
gh release download "v$VERSION" -R loomux/tui -p "$file" -p SHA256SUMS
```

or with curl:

```sh
base="https://github.com/loomux/tui/releases/download/v$VERSION"
curl -fL -O "$base/$file" -O "$base/SHA256SUMS"
```

## 3. Check it

First, check that the file matches `SHA256SUMS`. On Linux:

```sh
sha256sum -c --ignore-missing SHA256SUMS
```

On macOS:

```sh
shasum -a 256 -c --ignore-missing SHA256SUMS
```

It must print `loomux-…: OK`. If it prints `FAILED`, or `no file was
verified`, don't use the file: download it again.

Then check where it was built. The checksum only shows that the file
matches the `SHA256SUMS` next to it. The attestation shows that
`loomux/tui`'s release workflow built this exact file on GitHub Actions.
With the GitHub CLI, signed in (`gh auth login`):

```sh
gh attestation verify "$file" -R loomux/tui --signer-workflow loomux/tui/.github/workflows/release.yml
```

It exits 0 when the attestation checks out. For a file that wasn't built
there, it fails: one built anywhere else has no attestation (`HTTP 404:
Not Found`), and one built by another workflow doesn't match
`--signer-workflow`.

## 4. Put it on your PATH

For example, in `~/.local/bin`, which must be on your `PATH`:

```sh
mkdir -p ~/.local/bin
install -m 0755 "$file" ~/.local/bin/loomux
loomux version
```

`loomux version` prints the release and the server release it is pinned
to:

```
loomux 0.1.0
server pin v0.3.143
```

If macOS refuses to open a file you downloaded with a browser ("cannot
be opened because the developer cannot be verified"), remove its
quarantine flag. Files downloaded with `gh` or curl don't get one.

```sh
xattr -d com.apple.quarantine ~/.local/bin/loomux
```

To upgrade, do the same with the new version. The config and the
stored token stay where they are.

## 5. Sign in

`loomux login` signs the terminal in to the server through the
browser. It isn't in a release yet: see the README's
[Status](../README.md#status). The README's
[Configuration](../README.md#configuration) covers profiles and where
the token is kept.

## With go install

You need Go at the version in `go.mod` or later.

```sh
go install github.com/Loomux/tui/cmd/loomux@vX.Y.Z
```

This builds the release's source on your machine and puts `loomux` in
`$(go env GOBIN)`, or `$(go env GOPATH)/bin` when that's unset. Go
checks the source against its checksum database (`sum.golang.org`).
There's no attestation for a binary you built yourself.
`loomux version` prints the version Go records in the binary
(`loomux vX.Y.Z`).

Write the module path with a capital L, as `go.mod` declares it.
`github.com/loomux/tui` fails with `module declares its path as:
github.com/Loomux/tui`.
