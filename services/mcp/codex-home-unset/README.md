An empty stand-in for the Codex home. Compose mounts it read-only into the
processor when `LIFE_OS_CODEX_HOME_HOST_PATH` is unset, which only
`LIFE_OS_AI_PROVIDER=claude` allows: the Codex provider finds no `auth.json`
here and refuses to start.
