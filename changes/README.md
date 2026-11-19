# Changelog fragments

A pull request with a user-visible change adds its `CHANGELOG.md` entry
here as its own file instead of editing `CHANGELOG.md`, so open pull
requests never conflict over the same `[Unreleased]` lines. Each MINOR
milestone folds them into `CHANGELOG.md`, as in `loomux/server`. CI
checks them with `deploy/changelog.sh check` (the server's script).
Folding them in (`deploy/changelog.sh release`, with `CHANGELOG.md`)
starts with the release work (R3).

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
