## How this run may end

Every run ends in exactly **one** of four ways. Anything else — a silent stop, an unexplained exit, a card left "running" with no agent behind it — is a bug, not an outcome.

| Exit | Means | What you must do |
|---|---|---|
| `done` | The work is finished and verified. | Record the result and stop. |
| `retryable` | Infrastructure failed; the work itself is unaffected — a flaky test, a network blip, a container that died. | Say what failed and that it is retryable. Do **not** charge it against a retry budget. |
| `needs-input` | A decision only a human can make, and you can name what you already checked. | State the question and stop. Never guess a product decision to avoid stopping. |
| `needs-human-work` | The work is beyond this run: the blocker is real, named, and not something more attempts would fix. | Name the blocker and what a human needs to do next. |

`retryable` and `needs-human-work` are the two most often confused. Ask: *would running this again, unchanged, plausibly succeed?* If yes it is `retryable`; if no it is `needs-human-work`. A failure you have not diagnosed is not automatically retryable — say so honestly rather than inviting an endless loop.

Before returning, record the outcome so the next stage reads it as data instead of parsing your prose. **Substitute your own ticket key and stage name** — `SC-123` and `stage.fix` below are examples, not literal keys. A record written under `stage.<stage>` verbatim is invisible to the orchestrator, which looks up the concrete stage:

```bash
human state set SC-123 stage.fix --json --body-file - <<'EOF'
{"exit":"done",
 "summary":"one line — what happened",
 "evidence":"file:line, command output, or the marker that backs it",
 "next":"what the next stage or the human should do"}
EOF
```

This record is in addition to, not instead of, the `[human:*]` marker your stage already posts: the marker is the ticket's public trail, this is the machine-readable handoff.
