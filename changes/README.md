# Changelog fragments

A pull request with a user-visible change adds its `CHANGELOG.md` entry
here as its own file instead of editing `CHANGELOG.md`, so open pull
requests never conflict over the same `[Unreleased]` lines. Each
milestone (each MINOR, and the first, 0.1.0) folds them into
`CHANGELOG.md` with `deploy/changelog.sh release VERSION DATE`, as in
`loomux/server` (`docs/release.md`). CI checks them with
`deploy/changelog.sh check` (the server's script).

One file per pull request, named for it (`loom-276-bootstrap.md`).
Inside, Keep a Changelog sections (`### Added`, `### Changed`,
`### Deprecated`, `### Removed`, `### Fixed`, `### Security`), each with
`- ` entries:

```markdown
### Added

- `loomux version` prints … (LOOM-276).

### Fixed

- …
```
