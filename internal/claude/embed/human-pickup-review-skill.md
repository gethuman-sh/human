---
name: human-pickup-review
description: Pick up a PM ticket flagged with [human:ready-for-review] and run a peer review on each linked engineering ticket (or on the PM ticket itself when none is linked)
argument-hint: <pm-ticket-key>
---

You are picking up a code review handoff. The engineer (human or AI) who finished the work posted a structured comment on a PM ticket with the format:

```
[human:ready-for-review]
engineering: HUM-89, HUM-90
branch: main
commits: 2037e40, 64bb370
```

The `engineering:` line is present only in split topology (separate engineering tracker). In single-tracker topology it is absent — the review target is the PM ticket the comment sits on.

Your job: parse that comment, run the `human-reviewer` agent against each review key (each engineering key, or the PM key itself when the `engineering:` line is absent), then post a follow-up comment on the PM ticket summarising the outcome.

## Steps

1. **Resolve the PM ticket.** `$ARGUMENTS` is the PM ticket key (e.g. `SC-79`). Run `human tracker list` to find its tracker. Use the tracker marked with `role: pm` — if roles are not set, pick the tracker whose kind matches the key prefix (`SC-…` → Shortcut, `KAN-…`/issue-in-project → Jira, etc.).

2. **Read the latest handoff comment.** Run `human <pm-tracker> issue comment list <PM_KEY>`. Scan comments newest-first for a body starting with `[human:ready-for-review]`. If none is found, stop and report: `No ready-for-review handoff on <PM_KEY>`. Do not guess.

3. **Parse the block.** Extract:
   - `engineering:` — comma-separated engineering ticket keys. May be absent (single-tracker topology): the review keys are then just `<PM_KEY>` itself.
   - `branch:` — the branch the reviewer should be on.
   - `commits:` — short SHAs, for cross-checking.

   If the current branch does not match `branch:`, warn the user but proceed (the reviewer agent operates on the current branch; the user may have chosen to review from a different branch deliberately).

4. **Run the reviewer per review key.** For each key in `engineering:` (or for `<PM_KEY>` when the line is absent), invoke the existing reviewer agent via the Task tool:
   ```
   Task(subagent_type="human-reviewer", prompt="Review changes for ticket <REVIEW_KEY>")
   ```
   Each run writes `.human/reviews/<review_key_lowercased>.md`.

5. **Collect verdicts.** Open each `.human/reviews/<key>.md` the reviewer produced. The first line under `## Summary` is the verdict (`pass`, `pass with notes`, or `fail`). Roll them up into an overall verdict:
   - all pass → `pass`
   - any pass-with-notes, no fails → `pass with notes`
   - any fail → `fail`

6. **Post the follow-up comment on the PM ticket.** Use this format:
   ```
   [human:review-complete]
   verdict: <overall-verdict>
   reviews:
     <REVIEW_KEY_1>: <verdict> — .human/reviews/<review_key_1>.md
     <REVIEW_KEY_2>: <verdict> — .human/reviews/<review_key_2>.md
   ```
   Post it with `human <pm-tracker> issue comment add <PM_KEY> "<body>"`. The `[human:review-complete]` header mirrors the handoff header so future tooling can close the loop (e.g. auto-transition the reviewed tickets to Done when all reviews pass).

7. **Report to the user.** Summarise in plain text: which tickets were reviewed, the overall verdict, and where each review lives.

## Principles

- **Do not invoke `human-reviewer` on keys outside the handoff block.** Only review what the handoff claims. Scope creep in the review defeats the purpose of the convention.
- **Do not transition ticket status.** Closing/rejecting is the user's decision after reading the review. This skill reports, it does not decide.
- **Do NOT use `AskUserQuestion`.** Execute autonomously. If the handoff is missing or malformed, stop and report.
