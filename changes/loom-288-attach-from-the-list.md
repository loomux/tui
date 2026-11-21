### Added

- `a` on a conversation in the list attaches to its agent. Outside tmux
  the list leaves the terminal to the attach command and is back when
  you detach, saying what happened. Inside tmux the attach gets a tmux
  window of its own, named for the workspace, and the list stays up. A
  task another client holds is taken only after a yes on screen. A
  server that can't attach yet, and a machine that can run none of the
  server's commands, get the commands as text (LOOM-288).
- `[attach] mode` in the config chooses how the list attaches: `auto` (a
  tmux window inside tmux, else on this terminal), `inline` or `window`
  (LOOM-288).
- `loomux attach --keep-open` waits for enter after a failure, for a
  window that closes with the command (LOOM-288).
