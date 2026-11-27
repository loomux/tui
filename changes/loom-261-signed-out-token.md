### Fixed

- The list, its take back and attach, and `loomux attach` delete the
  stored token when the API answers a request made with it with its own
  401, and say to run `loomux login`. The token is compared and deleted
  in one step, so a login in another terminal keeps its new token
  (LOOM-261).
