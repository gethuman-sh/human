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

   Do NOT proceed against a branch that disagrees with `branch:`. The reviewer's Step 0 binding gate checks out the handoff branch itself and returns `unreviewable` on any mismatch — never review "the current branch as-is" hoping it is close enough, and never post a verdict for code that is not the handed-off branch.

4. **Run the reviewer per review key, bound to the handoff.** For each key in `engineering:` (or for `<PM_KEY>` when the line is absent), invoke the existing reviewer agent via the Task tool, threading the parsed `branch:`/`commits:` as the binding so the agent verifies the checkout before reviewing:
   ```
   Task(subagent_type="human-reviewer", prompt="Review changes for ticket <REVIEW_KEY> --branch=<branch> --commits=<commits>")
   ```
   Each run writes `.human/reviews/<review_key_lowercased>.md`. Review ONLY the keys named in this handoff, and post markers ONLY on `<PM_KEY>` — never on any ticket the handoff does not name.

5. **Collect verdicts.** Open each `.human/reviews/<key>.md` the reviewer produced. The first line under `## Summary` is the outcome (`pass`, `pass with notes`, `fail`, or `unreviewable: <reason>`). If ANY reviewed key's Summary starts with `unreviewable`, the reviewer could not obtain that code — skip the pass/notes/fail roll-up entirely and go to the unreviewable branch of step 6. Otherwise roll them up into an overall verdict:
   - all pass → `pass`
   - any pass-with-notes, no fails → `pass with notes`
   - any fail → `fail`

6. **Post the follow-up comment on the PM ticket.**

   **Unreviewable escape (leading branch).** If any reviewed key was `unreviewable`, do NOT post `[human:review-complete]` and do NOT dispatch any rework — nothing was reviewed, so a verdict would be a lie. Post `[human:review-failed]` instead, naming the unreachable ref(s) per affected key so the board renders an honest, retryable stage failure, then STOP:
   ```
   [human:review-failed]
   <REVIEW_KEY>: <reachability reason — e.g. handoff branch feat/x not found — no code was reviewed>
   ```
   Post it with `human <pm-tracker> issue comment add <PM_KEY> "<body>"`, tell the user how to make the code reachable (push the branch / commit with the ticket key), and STOP — do not run the pass/notes/fail posting below.
 The comment is the canonical record of the review — it must carry the reviewer's full findings inline so a reader (and the board detail panel) sees what was found without opening any local file. The `.human/reviews/<key>.md` files remain as working artifacts, not the source of truth. Use this format:
   ```
   [human:review-complete]
   verdict: <overall-verdict>
   reviews:
     <REVIEW_KEY_1>: <verdict> — .human/reviews/<review_key_1>.md
     <REVIEW_KEY_2>: <verdict> — .human/reviews/<review_key_2>.md

   ## Findings
   <the full findings for each reviewed key, inlined from the reviewer's output:
    what was checked, every issue found (or "no issues"), and any notes — the
    substance of each .human/reviews/<key>.md, not just a pointer to it>
   ```
   Post it with `human <pm-tracker> issue comment add <PM_KEY> "<body>"`. The `[human:review-complete]` header mirrors the handoff header so future tooling can close the loop (e.g. auto-transition the reviewed tickets to Done when all reviews pass), and the inlined `## Findings` section makes the on-ticket comment the complete review record.

7. **Report to the user.** Summarise in plain text: which tickets were reviewed, the overall verdict, and where each review lives.

## Principles

- **Do not invoke `human-reviewer` on keys outside the handoff block.** Only review what the handoff claims. Scope creep in the review defeats the purpose of the convention.
- **Do not transition ticket status.** Closing/rejecting is the user's decision after reading the review. This skill reports, it does not decide.
- **Do NOT use `AskUserQuestion`.** Execute autonomously. If the handoff is missing or malformed, stop and report.
