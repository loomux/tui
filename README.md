# loomux/tui

`loomux`, the terminal client for [Loomux](https://github.com/loomux/server).
It shows an attention-first list of conversations and lets you chat with a
live stream and answer blocked prompts and offers. You can also attach to a
running agent's tmux session from your own terminal and hand work back to
the web client.

## Status

**Pre-release, not usable yet.** So far there is only the repository
skeleton and `loomux version`. Logging in, the list, chat and attach are
added ticket by ticket, following the build plan in the design.

## Design

The design is
[`docs/design/tui-client-design.md`](https://github.com/loomux/server/blob/main/docs/design/tui-client-design.md)
in `loomux/server`. It stays in that repository, together with every API
change, because that's where the API is.

## Building from source

You need Go at the version in `go.mod` or later.

```sh
git clone https://github.com/loomux/tui.git
cd tui
go build ./cmd/loomux
./loomux version
```

`loomux version` prints the client's version and the
`github.com/Loomux/server` release it is pinned to, for example:

```
loomux v0.0.0-20261105205442-e9cf01ab4ff8
server pin v0.3.143
```

A release build prints its release version. A build from a git checkout
prints the pseudo-version Go stamps from the commit, and a build with no
version information prints `dev`.

The client pins the server as a Go module requirement on a release tag.
Only test files import it, and none of it is linked into the binary. After
bumping the pin (`go get github.com/Loomux/server@v0.3.N`), run
`go generate ./...` to update `internal/version/serverpin.go`. A test fails
if it doesn't match `go.mod`.

## License

[MIT](LICENSE)
