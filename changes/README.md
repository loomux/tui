# Changelog fragments

A pull request with a user-visible change adds its `CHANGELOG.md` entry
here as its own file instead of editing `CHANGELOG.md`, so open pull
requests never conflict over the same `[Unreleased]` lines. Each MINOR
milestone folds them into `CHANGELOG.md`, as in `loomux/server`. The
tooling for that (`deploy/changelog.sh`) and its CI check come with the
release work.

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
