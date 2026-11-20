### Added

- Releases (LOOM-278): a `vX.Y.Z` tag on `main` builds `loomux` for
  Linux and macOS (amd64 and arm64) with its version stamped in, and
  publishes the binaries on a GitHub pre-release with `SHA256SUMS` and a
  build provenance attestation for each (`gh attestation verify`). Only
  a pushed tag publishes. `CHANGELOG.md` holds the milestones; how to
  cut a release is in `docs/release.md`.
