---
name: human-autofix
description: Autonomously verify, reproduce, root-cause, and fix a reported bug end to end, handing off for review
argument-hint: <bug-ticket-key>
---

# Overview

Point this skill at a bug ticket and it runs the full bug-fix pipeline autonomously: **triage & reproduce → root-cause explanation on the ticket → verdict → (if a real bug) plan → test-first fix on a branch → verify → hand off for review**. The whole trail is recorded on the tracker (comments + the plan — a separate engineering ticket in split topology, a `[human:plan]` comment on the bug ticket itself otherwise); **no `.human/` working files** are produced.

The handoff is the same one the kanban executor posts — `[human:ready-for-review]` with the branch and commits, **no pull request**. Opening the PR, gating on CI, and merging all belong to the deploy stage (the board's Deploy drop/button, which drives push → PR → CI → merge → close), exactly as for feature work.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — reach a verdict and act on it (SC-86: "no further input"). Every run ends in exactly one verdict: **confirmed**, **not-a-bug**, or **undetermined**.

Follow these steps in order.

## Step 1 — Parse argument

`$ARGUMENTS` is the bug ticket key — the PM ticket. Call it `<BUG_KEY>`. Resolve its tracker with `human tracker list` (or just use `human get <BUG_KEY>` when only one tracker type is configured). Call the tracker `<tracker>`.

## Step 2 — Phase 1: Triage & reproduce (verdict)

Delegate to the **human-bug-triage** agent:

```
Task(subagent_type="human-bug-triage", prompt="Triage bug ticket <BUG_KEY>: reproduce it minimally, trace the full cause chain (symptom → proximate cause → underlying cause) with file:line evidence and the regression window, scan for sibling occurrences of the same defect pattern, and reach a verdict. Post the verdict comment on the ticket with a plain-language Explanation section a non-engineer can follow.")
```

It posts a `[human:bug-verdict] <verdict>` comment on the bug ticket — the ticket's permanent root-cause record: a plain-language explanation first, then the reproduction, the cause chain down to the underlying cause (not just the line that crashed), the regression window, and sibling occurrences. It returns the verdict (`confirmed` | `not-a-bug` | `undetermined`) plus, for a confirmed bug, the root cause and a fix outline. If the returned analysis stops at a proximate cause ("X is null" without *why* X can be null), re-dispatch the triage agent once, telling it which "why" is unanswered — do not carry a shallow root cause into the plan.

## Step 3 — Verdict gate

- **not-a-bug** — the agent has already posted its reasoning. Reclassify or close the ticket: discover statuses with `human <tracker> issue statuses <BUG_KEY>`, then move it with `human <tracker> issue status <BUG_KEY> "<closed-or-wontdo-status>"`. Make **no code changes**. Report and STOP.
- **undetermined** — the agent has posted an honest status (e.g. could not reproduce). Make **no code changes**. Leave the ticket open for a human. Report and STOP.
- **confirmed** — continue.

## Step 4 — Phase 2: Plan (topology decides where it lives)

1. Resolve the topology: from `human tracker list`, look for a tracker whose role is `engineering` that is a DIFFERENT tracker than the bug ticket's.
   - **Split topology** — it exists: note it and its first project (e.g. Linear project `HUM`) as `<ENG_TRACKER>` and `<ENG_PROJECT>`. The plan becomes a separate engineering ticket.
   - **Single-tracker topology** — no such tracker: the plan becomes a `[human:plan]` comment on the bug ticket itself; no second ticket.
2. Delegate to the **human-planner** agent, seeding it with the triage root cause:

```
Task(subagent_type="human-planner", prompt="Create an implementation plan to fix bug <BUG_KEY>. The root-cause analysis from triage:\n<paste the triage root cause + fix outline>\nThe plan's Changes section MUST begin with adding a regression test that fails because of the bug, then fixing the root cause. Return the plan as output; do not write files or create tickets.")
```

Capture the output as `<PLAN_CONTENT>`. Ensure its header has a `**PM ticket**: <BUG_KEY>` line and, in split topology, an `**Engineering ticket**: TBD` line.

3. Attach the plan.

**Split topology** — create the engineering ticket:

```bash
human <ENG_TRACKER> issue create --project=<ENG_PROJECT> "Fix: <short bug summary>" --description "$(cat <<'PLAN_EOF'
<PLAN_CONTENT>
PLAN_EOF
)"
```

Capture `<ENG_KEY>`, then update its description so the `**Engineering ticket**:` line reads `<ENG_KEY>` (replacing `TBD`). The fixer and verify agents read the plan from this ticket. Set `<WORK_KEY>` to `<ENG_KEY>`.

**Single-tracker topology** — post the plan as a `[human:plan]` comment on the bug ticket:

```bash
human <tracker> issue comment add <BUG_KEY> "$(cat <<'PLAN_EOF'
[human:plan]

<PLAN_CONTENT>
PLAN_EOF
)"
```

Verify with `human plan show <BUG_KEY>` — the fixer and verify agents read the plan the same way. Commits reference only `<BUG_KEY>`. Set `<WORK_KEY>` to `<BUG_KEY>`.

## Step 5 — Phase 3: Test-first fix

Delegate to the **human-bug-fixer** agent:

```
Task(subagent_type="human-bug-fixer", prompt="Fix ticket <WORK_KEY> (PM bug <BUG_KEY>) test-first on a feature branch and push it.")
```

It creates branch `autofix/<work-key>` (the key lowercased), writes a regression test that **fails** because of the bug, implements the root-cause fix, confirms the suite is green, commits referencing the ticket trail (both keys in split topology, the single bug key otherwise), pushes the branch, and returns the branch name. If it reports it could not reach a green build/test, STOP and report — do not open a PR.

## Step 6 — Phase 4: Verify (done gate)

Delegate to the **human-bug-verify** agent:

```
Task(subagent_type="human-bug-verify", prompt="Verify ticket <WORK_KEY> (PM bug <BUG_KEY>): confirm the regression test fails before / passes after the fix, the full suite is green, and the fix addresses the root cause. Post the verdict as a comment on <BUG_KEY>.")
```

If the verdict is NOT DONE, re-run Step 5 once to address the gaps; if it still fails, STOP and report honestly without posting the handoff.

## Step 7 — Phase 5: Hand off for review

Only after a DONE verdict. Post the review handoff comment on the bug (PM) ticket — the **same handoff the kanban executor posts**, and like there it deliberately opens NO pull request: the deploy stage (the board's Deploy drop/button) owns push → PR → CI gate → merge → close, for bug fixes exactly as for feature work. The format is fixed so it can be parsed across trackers; `<short-shas>` come from `git log --grep=<WORK_KEY> --format='%h' HEAD` (comma-separated). In single-tracker topology OMIT the `engineering:` line entirely — the reviewer works from the bug key the comment sits on:

```bash
human <tracker> issue comment add <BUG_KEY> "$(cat <<'HANDOFF_EOF'
[human:ready-for-review]
engineering: <ENG_KEY>
branch: autofix/<work-key>
commits: <short-shas>
HANDOFF_EOF
)"
```

Make sure the branch is pushed (Step 5 pushes it; verify with `git ls-remote --heads origin autofix/<work-key>`). If the handoff comment cannot be posted, STOP with an honest status report — **do not report success**.

## Step 8 — Summary

Report the verdict. For a confirmed fix, present the traceability chain:

```
Autofix complete for <BUG_KEY>

Verdict: confirmed
- PM bug:     <tracker> <BUG_KEY>
- Root cause: [human:bug-verdict] comment on <BUG_KEY> (explanation + cause chain)
- Plan:       <ENG_TRACKER> <ENG_KEY> (split topology) — or [human:plan] comment on <BUG_KEY>
- Branch:     autofix/<work-key>
- Handoff:    [human:ready-for-review] comment posted on <BUG_KEY> — review + deploy ship it
```
