### Added

- `loomux open ID` starts the list on that conversation, as the web's
  "open in terminal" command will show it. Until the conversation view
  lands it puts the cursor on the conversation's row (LOOM-289).
- `o` on the list opens the conversation in the browser, at
  `<server>/conversations/<id>`. Over ssh, or with no display, it shows
  the link to copy instead (LOOM-289).
- `T` on a conversation driven from another terminal takes it back: the
  server detaches that terminal and hands the task back, and the list
  says from whom. On such a row the footer shows who holds it and since
  when. The keymap's new action is `take_back` (LOOM-289).
