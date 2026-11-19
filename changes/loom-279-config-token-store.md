### Added

- The config file, `$XDG_CONFIG_HOME/loomux/config.toml`, with profiles
  chosen by `--profile`, `LOOMUX_PROFILE` or `default_profile`, and
  `LOOMUX_SERVER` to override the server URL (LOOM-279). Server URLs
  must be `https://`; `http://` is accepted only for loopback. The config
  never holds a token: unknown keys are errors.
- Token stores (LOOM-279): the OS keyring by default, or, with
  `token_store = "file"`, a `0600` file per profile under
  `$XDG_STATE_HOME/loomux/`. A file with wider permissions is refused.
  Without a keyring, `loomux` says how to opt in to the file store and
  never falls back on its own. No command uses them yet: `loomux login`
  comes next.
