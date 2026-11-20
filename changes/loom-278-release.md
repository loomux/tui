### Added

- Releases (LOOM-278): every merge to `main` publishes the next patch
  pre-release, and a milestone (0.1.0, each MINOR) is a `vX.Y.0` tag a
  person pushes. Each release builds `loomux` for Linux and macOS (amd64
  and arm64) with its version stamped in, and publishes the binaries on
  a GitHub pre-release with `SHA256SUMS` and a build provenance
  attestation for each (`gh attestation verify`). `CHANGELOG.md` holds
  the milestones. How releases work is in `docs/release.md`.
