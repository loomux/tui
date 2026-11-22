### Added

- A keymap (LOOM-280): every key is an action name with a vim-style
  default (`j`/`k`, `g`/`G`, `/` filter, `enter` open, `esc` back, `n`
  new, `a` attach, `w` watch, `y`/`n` answer, `x` cancel, `o` browser,
  `p` transcript, `?` help; in the composer `enter` sends and
  `alt+enter` adds a newline). `[keys]` in the config overrides any of
  them. An unknown action, an invalid key or two actions with the same
  key on one screen is an error at start, not silently ignored. `?`
  help is built from the effective map. No screen uses it yet: the home
  list comes next.
