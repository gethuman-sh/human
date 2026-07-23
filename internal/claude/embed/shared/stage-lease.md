## Take the stage lease first

Before doing any work, lease the stage. It is one command, and it is what makes a crashed run recoverable rather than repeated:

```bash
human state lease SC-123 --stage fix --agent "$HUMAN_AGENT_NAME" --json
```

Substitute your ticket key and your stage — `SC-123` and `fix` are examples.

Read the result:

- **`granted: false`** — another agent holds this stage and is still alive. Stop; you are a duplicate. Do not work in parallel with it.
- **`granted: true` with `displaced`** — the previous holder died. `inherited_keys` lists the state it left behind. **Read those keys before starting**: they are what it had already worked out, and redoing that work is the waste this exists to prevent.

  ```bash
  human state get SC-123 fix.evidence   # whatever inherited_keys named
  ```

- **`granted: true` with no `displaced`** — a clean start, nothing to inherit.

While you work, re-run the same lease command roughly every ten minutes. Re-leasing as the same agent refreshes the heartbeat and keeps your original lease time; without it a long stage looks abandoned and a later agent may take it over while you are still working.

Record what you learn as you go, not only at the end — state written before a crash is state your successor inherits, and state held only in your head is lost with the container.
