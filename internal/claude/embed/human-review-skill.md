---
name: human-review
description: Fetch a dispatched ticket and review its handoff branch's changes against its acceptance criteria
argument-hint: <dispatched-key> [--branch=…] [--commits=…]
---

`$ARGUMENTS` is `<DISPATCHED_KEY> [--branch=…] [--commits=…]`. The first token is the **dispatched key** — the ONE ticket this review is bound to. The optional `--branch=` and `--commits=` flags are the authoritative handoff binding the daemon derived: the exact branch and SHAs under review. Parse them out, then delegate to the **human-reviewer** agent, threading the binding through verbatim so the agent can verify the checked-out code IS this branch and these commits before reviewing:

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket <DISPATCHED_KEY> --branch=<branch> --commits=<commits>", run_in_background=false)
```

The dispatched key is fixed for the whole run. Every marker you post below goes on `<DISPATCHED_KEY>` and **no other ticket** — never re-derive a "PM ticket" from the reviewed diff or from whatever HEAD the worktree sits on. That re-derivation is the exact bug this binding closes: a review dispatched for one ticket must never post its verdict on another.

After the agent finishes:

1. **Read the verdict.** Open `.human/reviews/<key>.md` (the lowercased dispatched key). The first line under `## Summary` is the outcome: `pass`, `pass with notes`, `fail`, `incomplete`, `unreviewable: <reason>`, or `decision-required: <one-line fork>`. The last two are not verdicts — they are escapes, handled below BEFORE any verdict is posted.
2. **Handle the unreviewable escape FIRST.** If the Summary line starts with `unreviewable` (which includes every Step 0 binding failure — missing branch, unreachable commits, handoff mismatch), the reviewer could not obtain the bound code — nothing was reviewed. Do NOT post `[human:review-complete]` and do NOT dispatch any rework: there are no findings, and a `verdict: fail` would badge the card "review found problems" and feed a fixer against phantom findings. Instead post `[human:review-failed]` on the **dispatched key**, carrying the reachability reason, so the board renders an honest, retryable stage failure:
   ```bash
   human marker post <DISPATCHED_KEY> review-failed --field reason="<reachability reason, e.g. handoff branch feat/x not found — no code was reviewed>"
   ```
   Tell the user how to make the code reachable (push the branch / commit with the ticket key so the commits are reachable), then STOP. Reserve `[human:review-complete] verdict: fail` for reviews that actually examined code.
3. **Handle the decision-required escape next.** If the Summary line starts with `decision-required`, the review reached no verdict because the outcome is a genuine product or scope fork (e.g. "either build the re-run path or remove the menu item") — there is nothing to build or block, only a choice for a person to make. Post NO `[human:review-complete]` comment. Instead post the fork on the **dispatched key** so the board can render the choices and relaunch the picked one — options buried in prose are invisible to the pipeline:
   ```bash
   human marker post <DISPATCHED_KEY> options \
     --field stage="<planning | implementation | verification>" \
     --field context="<the decision-required one-liner>" \
     --field 1="<first option, one line>" \
     --field 2="<second option, one line>"
   ```
   This renders the `[human:options]` block with the fields in that order. `stage:` names which stage a choice relaunches (usually `implementation`). One line per option. Use this sparingly — only for a genuine fork the reviewer could not resolve, never as a substitute for a verdict. Then STOP; do not continue to step 4.
4. **Post the verdict on the dispatched key** so the board (and any watcher) can act on it:
   ```bash
   human marker post <DISPATCHED_KEY> review-complete --field verdict="<pass | pass with notes | fail | incomplete>" --field commits="<the --commits= SHAs you were dispatched with, plus any commit you made on the branch>" --body "<summary of the main findings>"
   ```
   This renders the fixed `[human:review-complete]` block with a `verdict:` line, parsed unambiguously across trackers. The findings summary is REQUIRED when the verdict is `fail` or `incomplete` — list each blocking finding (or each unmet acceptance criterion) as a bullet with its file reference; a rebuild is dispatched against exactly this comment, so it must contain everything needed to fix the problems. For `pass` and `pass with notes` verdicts one line suffices. `commits:` records WHICH commits this verdict judged — the dispatch binding you verified in Step 0, plus anything you committed yourself. A handoff re-posted later is compared against it, so a verdict with no commits leaves the board comparing timestamps.
5. **A fail or incomplete verdict is the answer, never also a question.** A criterion that fails, or one the ticket asked for but the commits do not meet, goes back to implementation through the verdict above — the board relaunches the build against your review-complete comment. Never post an options block alongside a `fail` or `incomplete` verdict, and never offer "accept as is and file a follow-on" against "fix it now": an unmet criterion is built, not deferred, and a question next to a verdict leaves the card waiting on a person for an answer the machine already has. By the time you reach this step you have already posted a verdict (step 4) or the decision-required escape (step 3) — never both; the escape in step 3 is the only place an options block belongs in this flow.
6. **Tell the user** the verdict and that the full review lives at `.human/reviews/<key>.md`.
