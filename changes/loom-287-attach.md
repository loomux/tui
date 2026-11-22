### Added

- `loomux attach <conversation|task>` (or `--task ID`) attaches this
  terminal to an agent's tmux session, always under a hold: the server
  knows who is in the pane and types nothing into it meanwhile. It runs
  the first of the server's attach commands whose program is on `PATH`
  (`kubectl`, `docker`, `ssh`, or `tmux` on the server's own machine),
  directly and never through a shell, and asks about the next one when a
  command fails at once. The hold is renewed every 15 seconds and
  released when the command ends, also on `SIGINT`, `SIGTERM` and
  `SIGHUP`. When the hold ends another way (taken back, taken over,
  expired, signed out), the attach is ended and `loomux` says which. A
  task another client holds is taken only after a yes, or with
  `--force`. On Linux the attach command is hung up if `loomux` is
  killed (LOOM-287).
- `[attach] prefer` in the config lists the ways in to use, the most
  preferred first, and `[attach.kubectl] context` adds `--context` to a
  kubectl command (LOOM-287).
