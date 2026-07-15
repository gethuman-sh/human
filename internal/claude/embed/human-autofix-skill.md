---
name: human-autofix
description: Autonomously verify, reproduce, root-cause, fix, review, and ship a reported bug end to end — a passing review ends in a merged PR
argument-hint: <bug-ticket-key>
---

# Overview

Point this skill at a bug ticket and it runs the full bug-fix pipeline autonomously: **triage & reproduce → root-cause explanation on the ticket → verdict → (if a real bug) plan → test-first fix on a branch → verify → review by the reviewer agent → (on a passing review) deploy: PR → CI gate → merge**. The whole trail is recorded on the tracker (comments + the plan — a separate engineering ticket in split topology, a `[human:plan]` comment on the bug ticket itself otherwise); the only `.human/` working file is the reviewer's `.human/reviews/<key>.md`.

The run does **not** end at the review handoff: exactly like the kanban flow — where a clean build chains straight into its review and Deploy ships it — the skill chains the fix into a review by the **human-reviewer** agent and, when the verdict is a pass, drives the same deploy pipeline the board's Deploy stage runs (push → PR → CI gate → merge → close). A failing review or a red CI gate stops the run honestly with the handoff left standing for a human.

**Board-context exception**: when this skill runs *as a board stage agent* (the `HUMAN_AGENT_NAME` environment variable is set and starts with `board-`), the daemon already chains the review on agent exit and the Bugs pane's Deploy button owns shipping — end at the handoff (Step 7.1) and skip Steps 7.2–8, or the review would run twice.

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

## Step 7 — Phase 5: Hand off and review

Only after a DONE verdict.

### 7.1 Post the review handoff

Post the review handoff comment on the bug (PM) ticket — the **same handoff the kanban executor posts**, so the trail and the board's `(R)` annotation work identically. The format is fixed so it can be parsed across trackers; `<short-shas>` come from `git log --grep=<WORK_KEY> --format='%h' HEAD` (comma-separated). In single-tracker topology OMIT the `engineering:` line entirely — the reviewer works from the bug key the comment sits on:

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

**Board-context exception applies here**: if `HUMAN_AGENT_NAME` starts with `board-`, STOP after this handoff (report per Step 9) — the daemon chains the review and the Deploy button ships it.

### 7.2 Review by the reviewer agent

Chain straight into the review, like the kanban flow chains a clean build. Post the started marker, then dispatch the reviewer:

```bash
human <tracker> issue comment add <BUG_KEY> "[human:review-started]"
```

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket <WORK_KEY>: check out branch autofix/<work-key> and review its diff against main against the ticket's plan and acceptance criteria.")
```

The reviewer writes `.human/reviews/<work-key>.md`; the first line under its `## Summary` is the verdict — `pass`, `pass with notes`, or `fail`. Post the outcome on the bug ticket (same follow-up the review pickup flow posts):

```bash
human <tracker> issue comment add <BUG_KEY> "$(cat <<'REVIEW_EOF'
[human:review-complete]
verdict: <verdict>
reviews:
  <WORK_KEY>: <verdict> — .human/reviews/<work-key>.md
REVIEW_EOF
)"
```

### 7.3 Review gate

- **pass** or **pass with notes** — continue to Step 8.
- **fail** — feed the reviewer's findings back once: re-dispatch the **human-bug-fixer** (Step 5) with the review findings appended to the prompt, re-run the verify gate (Step 6), then re-run the review (7.2, one new `[human:review-complete]` comment). If the second verdict still fails, STOP honestly: the `[human:ready-for-review]` handoff stays standing for a human, and NO pull request is merged.

## Step 8 — Phase 6: Deploy — end with a merged PR

Only after a passing review. This is the board's deploy pipeline (push → PR → CI gate → merge → close) driven from the skill:

1. Post the started marker: `human <tracker> issue comment add <BUG_KEY> "[human:deploy-started]"`.
2. Open the pull request with `human pr create --head autofix/<work-key> --title "[<BUG_KEY>] [<ENG_KEY>] <short summary>" --body ...` (single-tracker: only `[<BUG_KEY>]`). The body carries Summary / Verdict / Tests and the ticket key(s). If no forge is configured in `.humanconfig` and the origin remote is GitHub, fall back to `gh pr create` — never report success without a PR. Capture `<PR_URL>` and the PR number.
3. Gate on CI: `gh pr checks <number> --watch` (or the forge's equivalent). A failing gate → post `[human:deploy-failed]` with the reason on `<BUG_KEY>` and STOP — do NOT merge red.
4. Merge and clean up: `gh pr merge <number> --merge --delete-branch`. If the merge is blocked (required approvals, branch protection), post `[human:deploy-failed]` with the reason and STOP honestly — the PR stays open for a human.
5. Record success: `human <tracker> issue comment add <BUG_KEY> "[human:deployed]"` with a second line `pr: <PR_URL>`, then move the ticket to its done-type status (`human <tracker> issue statuses <BUG_KEY>`, then `human <tracker> issue status <BUG_KEY> "<done-status>"`). In split topology close `<ENG_KEY>` the same way.

## Step 9 — Summary

Report the verdict. For a confirmed, shipped fix, present the traceability chain:

```
Autofix complete for <BUG_KEY>

Verdict: confirmed — review: <verdict> — shipped
- PM bug:     <tracker> <BUG_KEY>
- Root cause: [human:bug-verdict] comment on <BUG_KEY> (explanation + cause chain)
- Plan:       <ENG_TRACKER> <ENG_KEY> (split topology) — or [human:plan] comment on <BUG_KEY>
- Branch:     autofix/<work-key>
- Review:     [human:review-complete] verdict: <verdict> on <BUG_KEY>
- PR:         <PR_URL> — merged, branch deleted
- Ticket:     moved to <done-status>
```

For a board-context run (exception in Step 7.1) or a failed review/deploy gate, report where the pipeline stopped, which marker records it, and what a human needs to do next.
