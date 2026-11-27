### Fixed

- The API's 401 deletes the stored token even when it answers a request
  that was still on its way as the screens ended: the list's poll, the
  workspaces, an attach's task lookup, a take back and the open
  conversation's fetches and stream reads are waited for, a few seconds at
  most, and their answers kept, as a takeover's already was. An answer's
  401 is applied whatever became of the view that asked it, including a
  transcript or conversation left before the answer arrived (LOOM-261).
