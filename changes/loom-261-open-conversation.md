### Added

- `enter` on the list, and `loomux open ID`, open the conversation
  screen: the messages, the live stream, the tasks' status and, with
  `p`, a task's transcript. `esc` goes back to the list, which goes on
  polling behind the conversation and is polled at once on the way back.
  A refused token (401) closes the conversation and stops the polling,
  and the list says to sign in again (LOOM-261).
