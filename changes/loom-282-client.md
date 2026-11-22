### Added

- The typed API client, `client/` (LOOM-282): the calls the TUI makes
  (conversations, send, dispatches, cancel, attach-info, takeover,
  heartbeat, release, transcripts, sessions, logout), the server's
  `{error, code}` envelope with each status's default for a code it
  doesn't know, and an auth proxy's page or redirect reported as such
  rather than as a parse error. It follows no redirect. A message is
  sent with an `Idempotency-Key` and retried with the same key, so it's
  never sent twice. The conversation stream reconnects with backoff
  (1 s doubling to 15 s) and drops a connection that misses three
  heartbeats. No command uses it yet.

### Changed

- The client is built and tested against server v0.3.155, the first
  release with `api/apitest`, and on Go 1.27.4, which that release
  requires.
