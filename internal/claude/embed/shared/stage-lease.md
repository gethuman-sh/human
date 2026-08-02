## Take the stage lease first

Before doing any work, lease the stage. It is one command, and it is what makes a crashed run recoverable rather than repeated:

```bash
human state lease <TICKET_KEY> --stage <STAGE> --agent "$HUMAN_AGENT_NAME" --json
```

`<STAGE>` above is **your** stage — it is already filled in for you, and it identifies your step of the pipeline. Do not change it to anything else. A lease is held per (ticket, stage), so a stage name that belongs to another step would lock that step out of its own work and hand you its leftovers as if they were yours.

Read the result:

- **`granted: false`** — another agent holds this stage and is still alive. It is running the same step on the same ticket, so it is you. Stop immediately and end the run as a no-op: record `exit:"done"` with a summary saying a live holder already owns the stage, and stop. Do not work in parallel with it.

  A refused lease is **scheduling, not a decision**. Never post a `[human:options]` block for it, never post a `*-failed` marker, and never leave the card waiting on a human — nobody can answer "another agent is already doing this", and a card parked on it stops the pipeline for a condition that resolves itself.

- **`granted: true` with `displaced`** — the previous holder of *this same stage* died. `inherited_keys` lists the state it left behind. **Read those keys before starting**: they are what it had already worked out, and redoing that work is the waste this exists to prevent.

  ```bash
  human state get <TICKET_KEY> <STAGE>.evidence   # whatever inherited_keys named
  ```

- **`granted: true` with no `displaced`** — a clean start, nothing to inherit.

While you work, re-run the same lease command roughly every ten minutes. Re-leasing as the same agent refreshes the heartbeat and keeps your original lease time; without it a long stage looks abandoned and a later agent may take it over while you are still working.

Record what you learn as you go, not only at the end — state written before a crash is state your successor inherits, and state held only in your head is lost with the container. Namespace every key you write under your own stage (`<STAGE>.…`), so what your successor inherits is unambiguously yours.
