## How this run may end

Every run ends in exactly **one** of five ways. Anything else — a silent stop, an unexplained exit, a card left "running" with no agent behind it — is a bug, not an outcome.

| Exit | Means | What you must do |
|---|---|---|
| `done` | The work is finished and verified. | Record the result and stop. |
| `retryable` | A flaky test or a container that died — the run itself can just be tried again. | Say what failed and that it is retryable. Do **not** charge it against a retry budget. |
| `outage` | The substrate a run depends on was unreachable — a credential store that timed out, a tracker it could not reach. Nothing was attempted. | Record `exit:"outage"`. It is not charged against any budget; the daemon retries with backoff until the substrate returns. One case is the daemon's to correct, not yours: where its own proxy refused a host by policy, a blocked connection looks exactly like a dead network from inside the container — it then reds the card once naming the host and the `proxy.domains` line to add, rather than waiting. |
| `needs-input` | A decision only a human can make, and you can name what you already checked. | State the question and stop. Never guess a product decision to avoid stopping. For a board stage (planning, implementation, verification), this exit recorded with no matching open `[human:options]` block on the ticket is read as an incomplete stop rather than a real question, and the board relaunches the stage instead of leaving the card waiting on nobody — post the block before recording the exit. The PR-fix and deploy-fix loop steps are not board stages and never relaunch: the loop raises the block itself from the `options` in your stage record, so record your directions there and do not post a block of your own. |
| `needs-human-work` | The work is beyond this run: the blocker is real, named, and not something more attempts would fix. | Name the blocker and what a human needs to do next — with evidence, not a verdict. Record a `blocker` object in the stage record and, when your stage posts a board marker for this stop (a `*-failed` marker), the same four as fields on it: `kind` (one of `missing-permission`, `unavailable-dependency`, `exhausted-fix-rounds`, `conflicting-requirements`, `other`), `evidence` (what you observed, verbatim — the command and its output, the file and line), `attempted` (what you tried before stopping), `release` (the condition under which the work can proceed, stated so a person or a later run can check it). Before choosing this exit, check what can be checked: a branch or commit you cannot resolve is a discovery step (`human commits for`, `git fetch`), not a blocker; a credential store or tracker that could not be reached is an `outage`; a permission the container lacks is a blocker only if you name the permission. A stop with no evidence is read as an unexplained stop, and a person who cannot tell from the ticket what to do will re-run the investigation you already did. |

`retryable` and `needs-human-work` are the two most often confused. Ask: *would running this again, unchanged, plausibly succeed?* If yes it is `retryable`; if no it is `needs-human-work`. A failure you have not diagnosed is not automatically retryable — say so honestly rather than inviting an endless loop.

An `outage` is distinct from `retryable`: the substrate — not the work's own tooling — was down, so it retries indefinitely with backoff rather than against a bounded budget. A tracker or credential store that could not be reached is an `outage`; a flaky test or a container that died mid-run is `retryable`.

Before returning, record the outcome so the next stage reads it as data instead of parsing your prose. **Substitute your own ticket key and stage name** — `<TICKET_KEY>` and `stage.fix` below are examples, not literal keys. A record written under `stage.<stage>` verbatim is invisible to the orchestrator, which looks up the concrete stage:

```bash
human state set <TICKET_KEY> stage.fix --json --body-file - <<'EOF'
{"exit":"done",
 "summary":"one line — what happened",
 "evidence":"file:line, command output, or the marker that backs it",
 "next":"what the next stage or the human should do"}
EOF
```

A `needs-human-work` record carries the blocker as data, so the stop can be audited and, when its release condition is checkable, lifted without a person:

```bash
human state set <TICKET_KEY> stage.fix --json --body-file - <<'EOF'
{"exit":"needs-human-work",
 "summary":"cannot push: the forge refuses the branch",
 "blocker":{"kind":"missing-permission",
   "evidence":"git push origin autofix/x → remote: Permission to gethuman-sh/human.git denied to humanbot (403)",
   "attempted":"retried once; checked `human doctor` (forge token resolves); confirmed the branch exists locally",
   "release":"the forge token gains write access to gethuman-sh/human, or a person pushes the branch"},
 "next":"grant the token write access, then Retry the stage"}
EOF
human marker post <TICKET_KEY> implementation-failed --field reason="cannot push: the forge refuses the branch" --field kind=missing-permission --field evidence="…" --field attempted="…" --field release="…"
```

Only the run that owns the stage posts that marker. If a skill dispatched you for one step of its stage — planner, reviewer, triage, verifier, second opinion, skeptic — you post no board marker for this stop: record the `blocker` in your stage record and exit, and the skill posts the stage's `*-failed` marker when its own budget is spent, with its spent budget as the blocker. A marker posted from inside a step reds the card before the skill has finished deciding, and one posted from the wrong stage reds a stage the card is not in. For the run that owns the stage, the marker is the one the board reads for it — `planning-failed` for planning, `implementation-failed` for implementation (the example above), `review-failed` for verification, `deploy-failed` for the deploy stage. There is no stage placeholder to substitute: a name the protocol does not define posts a comment the board never classifies, and the card keeps running with nothing behind it. The loop steps of the done stage (PR review, PR fix, deploy fix) post no failed marker either. The deploy-fix step records the blocker and the loop copies the four fields onto the `deploy-failed` marker it posts. The PR-review and PR-fix steps have no `needs-human-work` exit. From `stage.pr-review` the loop reads `verdict`, `head` and `findings`; a one-line fingerprint of the newest blocking finding reaches the ticket as the `finding:` field of each `[human:pr-fix-started]` marker (its file and class as `class:`) and as the `pr-review-failed` reason when the same finding, or the same class in the same file, repeats; the reviewer's full findings text does not reach the ticket, but every round's blocking findings and the fixer's exit are kept in the daemon's findings record. From `stage.pr-fix` the fixer's `deferred` leads the decision block on a `needs-input` stop with options and is the failed marker's reason on one without; on the round budget the loop reds the card with a canned headline. A finding that genuinely needs a person is a `needs-input` stop with 2+ `options` — the only route that puts the question on the ticket. `human fsm where <TICKET_KEY>` prints the command for the state you are in.

This record is in addition to, not instead of, the `[human:*]` marker your stage already posts: the marker is the ticket's public trail, this is the machine-readable handoff.
