---
name: human-review
description: Fetch a ticket and review the current branch's changes against its acceptance criteria
argument-hint: <ticket-key>
---

Delegate to the **human-reviewer** agent using the Task tool:

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket $ARGUMENTS")
```

After the agent finishes:

1. **Read the verdict.** Open `.human/reviews/<key>.md` (the lowercased ticket key). The first line under `## Summary` is the verdict: `pass`, `pass with notes`, or `fail`.
2. **Determine the PM ticket.** Fetch the reviewed ticket with `human get <KEY>`. In split topology the engineering ticket's description references the PM ticket (e.g. `SC-79`) — that is the PM key. In single-tracker topology (no such reference, or the plan lives in a `[human:plan]` comment) the key you were given IS the PM key.
3. **Post the verdict on the PM ticket** so the board (and any watcher) can act on it. The format is fixed so it can be parsed unambiguously across trackers:
   ```
   [human:review-complete]
   verdict: <pass | pass with notes | fail>

   <summary of the main findings>
   ```
   The findings summary is REQUIRED when the verdict is `fail` — list each blocking finding as a bullet with its file reference; a rebuild is dispatched against exactly this comment, so it must contain everything needed to fix the problems. For `pass` verdicts one line suffices.
   Post it with `human <pm-tracker> issue comment add <PM_KEY> "<comment-body>"`.
4. **Offer choices when the outcome is a genuine fork.** When the review ends in a decision between alternatives rather than one clear direction (e.g. "either build the re-run path or remove the menu item"), post a SECOND comment with a machine-readable options block so the board can render the choices and relaunch the picked one — options buried in prose are invisible to the pipeline:
   ```
   [human:options]
   stage: <planning | implementation | verification>
   context: <one line: why a decision is needed>
   1: <first option, one line>
   2: <second option, one line>
   ```
   `stage:` names which stage a choice relaunches (usually `implementation`). One line per option; the full reasoning stays in the review-complete comment above. Use this sparingly — only for real forks the user must decide, never as a substitute for a verdict.
5. **Tell the user** the verdict and that the full review lives at `.human/reviews/<key>.md`.
