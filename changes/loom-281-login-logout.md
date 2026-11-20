### Added

- `loomux login` signs a profile in from a browser, with no password in
  the terminal (LOOM-281). The terminal makes its own token and shows a
  link and a code. The link carries only the token's hash, and the
  web's `/link` page shows the same code before you approve. The token
  is never printed, and it is stored only once the server has accepted
  it: after ctrl+c, or 10 minutes without an approval, nothing is
  stored. The link is opened in a browser when there is a display and
  always printed; `--no-browser` only prints it. It needs server
  v0.3.151 or later and a web client with the `/link` page.
- `loomux logout` ends the session on the server and deletes the stored
  token. If the server can't be reached, the token is deleted all the
  same, and it says the session may still exist and exits 1.
- A stored token the server answers 401 to is deleted from the token
  store. `loomux login` then starts over; the screens will show "signed
  out" for it.
