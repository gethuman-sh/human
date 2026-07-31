---
name: human-pr-review
description: Run the machine PR review — dispatch the adversarial reviewer on the open draft PR and record its verdict
argument-hint: <key> --pr=<number> --branch=<branch>
---

`$ARGUMENTS` is `<KEY> --pr=<number> --branch=<branch>` — the PM ticket key, the open (draft) pull request to review, and its branch, all supplied by the daemon's deploy loop. Parse them, then delegate to the **human-pr-reviewer** agent.

Run the reviewer at the `opus` tier: it is the adversarial gate before a merge, and a weaker model gets talked out of real objections, turning the check into a rubber stamp (never tier down an adversary).

```
Task(subagent_type="human-pr-reviewer", model="opus", prompt="Review PR <number> for ticket <KEY> --branch=<branch>")
```

The agent reviews the fixer's **local** branch commit (not the stale pushed head — the fixer never pushes in board context, so origin's head is pre-fix), records its findings and the machine verdict (`approved | changes-requested | unreviewable`) in `stage.pr-review`, and mirrors findings onto the PR as inline comments when it has a write path. The daemon's loop reads that verdict to decide the next step — another fix pass, or the merge — so you do **not** post board markers, dispatch a fixer, or merge anything yourself: run the agent and report its verdict. Human review of the PR happens out of band and never gates this run.
