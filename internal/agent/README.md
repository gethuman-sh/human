# AI Developer Agents

`human` runs Claude Code as a background developer inside isolated Docker containers. Point an agent at a task or a ticket and let it work on its own, then check on it whenever you like.

- Launch a named agent on a task in a container
- Turn a tracker ticket straight into an agent task
- Pick which Claude model the agent runs
- Run unattended or interactively in the foreground
- Mount your project into the agent's container
- List all agents with status and running time
- Attach to a live agent to watch it work
- Send a follow-up instruction to a running agent with `human agent send <name> "message"` (continues its Claude session)
- Stop or delete an agent and its container
- Keep a per-run execution log on the host — the launch (prompt, argv, model), the agent's full output stream, the Claude session transcript (copied out before the container is removed), and the outcome — so even a crashed or reaped run stays analyzable
- Review past runs with `human agent logs <name>` (`--json` for raw records), or stream a running agent's output live with `--follow` (`--tail N` to seek back)
