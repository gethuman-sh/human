# Claude Code Integration

`human` plugs its planning and review skills into Claude Code and watches every Claude session you have running. You get the full `human` workflow inside Claude plus a live dashboard of what each session is doing.

- Install human's skills into Claude Code
- Add the supporting agents those skills use
- Install project-local or personal/global
- Register session-tracking hooks automatically
- Discover all running Claude sessions, including containers
- Show live status: working, idle, blocked, or errored
- See the current tool, subagents, and task list
- Report token usage for the current window across every transcript root on the host — the operator's own sessions and each registered project's agent container

Agent containers keep their Claude state in the project (`<project>/.devcontainer/claude`) rather than in the operator's home directory, so usage and cost read both trees; only each root's `projects/` subtree is walked, never the credential store beside it.

`Install` also writes the bot git identity (`GIT_AUTHOR_*` / `GIT_COMMITTER_*`, resolved from `botidentity` and the project `.humanconfig` `bot:` section) into the project `.claude/settings.json` `env` block, so host-run agent commits attribute to the bot while a developer's own terminal commits keep their identity; the session-tracking hooks continue to go to the global `~/.claude/settings.json`.
