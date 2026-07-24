# Bot Identity

Resolves the git identity attributed to pipeline agent commits, so authorship
alone separates agent work from developer work.

## Capabilities

- Reads the `bot:` section (`name`, `email`) from `.humanconfig`, falling back to
  a sensible default (`humanbot <humanbot@users.noreply.gethuman.sh>`) so
  attribution works before anyone configures anything.
- Produces the `GIT_AUTHOR_*` / `GIT_COMMITTER_*` environment pairs the daemon
  injects into every agent's exec, without touching any repository's shared git
  config — developer commits keep their own identity.
