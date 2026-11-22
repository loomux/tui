### Added

- The conversation screen (LOOM-284): a conversation's messages, with
  replies rendered as markdown, kept current by its event stream (the
  turn in flight, its reply as soon as the stream has it, each task's
  status line). The header says `reconnecting…` once the stream has
  been down for 5 s, and the conversation is fetched again when it is
  back. Scrolling to the top loads older messages. `p` shows a task's
  transcript: what was sent to the agent, its answer and its pane at
  the end of each turn. Text from the server is drawn with its escape
  sequences removed. The keys are the keymap's, and `?` shows the ones
  in effect. No command opens the screen yet: the home list comes
  next.
