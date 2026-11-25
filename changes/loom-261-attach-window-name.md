### Fixed

- The tmux window `a` opens is named for the workspace even when the
  workspace names haven't arrived yet: the attach waits for them instead
  of naming the window after the start of the workspace's id (LOOM-261).
