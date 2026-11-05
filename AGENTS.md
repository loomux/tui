# loomux/tui: the Loomux terminal client

This repo is `loomux`, the terminal (Bubble Tea) client for **Loomux**. Loomux
is a chat-driven interface to on-demand, tmux-backed coding agents. The
server, its API and its web client live in `loomux/server` and `loomux/web`.
This client talks to the server only over its public HTTP API (`/api/v1`).

`CLAUDE.md` is a symlink to this file.

## Design

The approved design is `docs/design/tui-client-design.md` in
**`loomux/server`**, not in this repo. It is the authority for this client:
its architecture, the repository layout, versions and compatibility,
releases, and the build plan (tickets R1-R3, T1…). If building something
shows the design needs to change, don't silently diverge. Say so in your
handoff (see below), and the change goes to the design doc through a PR in
`loomux/server`.

## Status

**Pre-release.** R1 (repository bootstrap) gives the module, `cmd/loomux`
with only `version`, and the generated server pin. CI (R2) and releases (R3)
come next.

## The server pin

- `go.mod` requires `github.com/Loomux/server` at a **release tag**
  (`v0.3.N`): never a pseudo-version, never a `replace` directive. To develop
  against an unmerged server change, use a local `go.work` that points at a
  server checkout. It is git-ignored and never committed.
- **Only `_test.go` files import the server.** None of it may be linked
  into the binary.
- After bumping the pin (`go get github.com/Loomux/server@v0.3.N`), run
  `go generate ./...` and commit `internal/version/serverpin.go`. A test
  fails if it doesn't match `go.mod`.
- A cross-repo feature merges in the server first and gets its tag. Then the
  client PR bumps the pin together with the code that uses the new API.

## Cross-workspace boundary (critical)

**This workspace must NEVER write to Vikunja, Plane or Trilium.** Only
**command-center** is authorized to create, update or close items there.
That's what keeps labels, dedup, resolution-doc format and Vikunja↔Plane
reconciliation consistent. Do not improvise REST or token access to those
systems, even if credentials are visible in context.

**Mechanism: `agent-mailbox`.** This workspace's mailbox name is
**`loomux-tui`**. When you finish (or get blocked on) ticket-relevant work,
send a `handoff` to command-center. Run `mailctl.py` from your local
`agent-mailbox` checkout:

```bash
python3 <agent-mailbox>/tools/mailctl.py send --from loomux-tui --to command-center \
    --type handoff --title "short topic" --body-file <file> --tickets LOOM-N
```

Body: what you did, commits and PRs, verification evidence, blockers, and
what remains. The full protocol is `RULES.md` in `agent-mailbox`. **Check
your inbox at the start of every session:**

```bash
python3 <agent-mailbox>/tools/mailctl.py inbox loomux-tui
```

Process or archive anything pending before starting new work.

This work maps to Vikunja project **`Loomux`** (id `7`) and Plane project
**`Loomux`** (identifier `LOOM`).

**Where things go on GitHub:**

- Issues about the terminal client live in **`loomux/tui`**.
- Any API change (a new route, field or error code the client needs) goes
  to **`loomux/server`**, as an issue or PR there, never as a workaround in
  the client. The design doc changes there too.
- Nothing in this workspace edits `loomux/server` or `loomux/web`.

## Public repository

This repo is public. Commit no internal host names, IP addresses, usernames
or personal paths: not in code, tests, docs, changelog fragments, commit
messages or PR text. Use `example.net` / `example.org` hosts in examples.

## Engineering

- Go at the version in `go.mod`; `gofmt`, `go vet` and `go test -race ./...`
  are clean before a PR.
- Each PR with a user-visible change adds a changelog fragment in
  `changes/` (see `changes/README.md`).
- Add a dependency only when code uses it. The UI stack (Bubble Tea,
  Bubbles, Lip Gloss, Glamour) arrives with the first UI ticket.
- Standard engineering workflow applies (TDD, etc.). Don't reopen decisions
  the design settled without a concrete reason found while building.
