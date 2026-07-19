---
name: human-review
description: Fetch a dispatched ticket and review its handoff branch's changes against its acceptance criteria
argument-hint: <dispatched-key> [--branch=…] [--commits=…]
---

`$ARGUMENTS` is `<DISPATCHED_KEY> [--branch=…] [--commits=…]`. The first token is the **dispatched key** — the ONE ticket this review is bound to. The optional `--branch=` and `--commits=` flags are the authoritative handoff binding the daemon derived: the exact branch and SHAs under review. Parse them out, then delegate to the **human-reviewer** agent, threading the binding through verbatim so the agent can verify the checked-out code IS this branch and these commits before reviewing:

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket <DISPATCHED_KEY> --branch=<branch> --commits=<commits>")
```

The dispatched key is fixed for the whole run. Every marker you post below goes on `<DISPATCHED_KEY>` and **no other ticket** — never re-derive a "PM ticket" from the reviewed diff or from whatever HEAD the worktree sits on. That re-derivation is the exact bug this binding closes: a review dispatched for one ticket must never post its verdict on another.

After the agent finishes:

1. **Read the verdict.** Open `.human/reviews/<key>.md` (the lowercased dispatched key). The first line under `## Summary` is the outcome: `pass`, `pass with notes`, `fail`, or `unreviewable: <reason>`.
2. **Handle the unreviewable escape FIRST.** If the Summary line starts with `unreviewable` (which includes every Step 0 binding failure — missing branch, unreachable commits, handoff mismatch), the reviewer could not obtain the bound code — nothing was reviewed. Do NOT post `[human:review-complete]` and do NOT dispatch any rework: there are no findings, and a `verdict: fail` would badge the card "review found problems" and feed a fixer against phantom findings. Instead post `[human:review-failed]` on the **dispatched key**, with the reachability reason on its first body line, so the board renders an honest, retryable stage failure:
   ```
   [human:review-failed]
   <reachability reason, e.g. handoff branch feat/x not found — no code was reviewed>
   ```
   Post it with `human <tracker> issue comment add <DISPATCHED_KEY> "<comment-body>"`, tell the user how to make the code reachable (push the branch / commit with the ticket key so the commits are reachable), then STOP. Reserve `[human:review-complete] verdict: fail` for reviews that actually examined code.
3. **Post the verdict on the dispatched key** so the board (and any watcher) can act on it. The format is fixed so it can be parsed unambiguously across trackers:
   ```
   [human:review-complete]
   verdict: <pass | pass with notes | fail>

   <summary of the main findings>
   ```
   The findings summary is REQUIRED when the verdict is `fail` — list each blocking finding as a bullet with its file reference; a rebuild is dispatched against exactly this comment, so it must contain everything needed to fix the problems. For `pass` verdicts one line suffices.
   Post it with `human <tracker> issue comment add <DISPATCHED_KEY> "<comment-body>"`.
4. **Offer choices when the outcome is a genuine fork.** When the review ends in a decision between alternatives rather than one clear direction (e.g. "either build the re-run path or remove the menu item"), post a SECOND comment on the **dispatched key** with a machine-readable options block so the board can render the choices and relaunch the picked one — options buried in prose are invisible to the pipeline:
   ```
   [human:options]
   stage: <planning | implementation | verification>
   context: <one line: why a decision is needed>
   1: <first option, one line>
   2: <second option, one line>
   ```
   `stage:` names which stage a choice relaunches (usually `implementation`). One line per option; the full reasoning stays in the review-complete comment above. Use this sparingly — only for real forks the user must decide, never as a substitute for a verdict.
5. **Tell the user** the verdict and that the full review lives at `.human/reviews/<key>.md`.
