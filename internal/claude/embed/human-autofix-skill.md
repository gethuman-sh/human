---
name: human-autofix
description: Autonomously verify, reproduce, root-cause, fix, review, and ship a reported bug end to end — a passing review ends in a merged PR
argument-hint: <bug-ticket-key>
---

# Overview

Point this skill at a bug ticket and it runs the full bug-fix pipeline autonomously: **triage & reproduce → root-cause explanation on the ticket → verdict → (if a real bug) plan → test-first fix on a branch → verify → review by the reviewer agent → (on a passing review) deploy: PR → CI gate → merge**. The whole trail is recorded on the tracker (comments + the plan — a separate engineering ticket in split topology, a `[human:plan]` comment on the bug ticket itself otherwise), and every run that attempted a fix ends by posting a plain-language `[human:fix-summary]` comment on the ticket (Step 9); the only `.human/` working file is the reviewer's `.human/reviews/<key>.md`.

The run does **not** end at the review handoff: exactly like the kanban flow — where a clean build chains straight into its review and Deploy ships it — the skill chains the fix into a review by the **human-reviewer** agent and, when the verdict is a pass, drives the same deploy pipeline the board's Deploy stage runs (push → PR → CI gate → merge → close). A failing review or a red CI gate stops the run honestly with the handoff left standing for a human.

**Board-context exception**: when `<BOARD_CONTEXT>` is true (launched with `--board`; `HUMAN_AGENT_NAME` starting with `board-` is a fallback signal), this skill runs *as a board stage agent*. The container holds no push/PR credentials and the Bugs pane's Deploy button owns shipping, so **end after the review (Step 7.3) and skip Step 8 (deploy) entirely**. The review itself now runs **inline, in this warm container** (Steps 7.2–7.3) — same workspace and caches the fix was built in — so a bug pays **one** container startup, not two (SC-782). Do NOT push, open, or merge a PR in board context.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — reach a verdict and act on it (SC-86: "no further input"). Every run ends in exactly one verdict: **confirmed**, **not-a-bug**, or **undetermined**.

Follow these steps in order.

## Step 1 — Parse argument

`$ARGUMENTS` is the bug ticket key — the PM ticket — optionally followed by `--board`. Take the first non-flag token as `<BUG_KEY>`. Resolve the bug ticket with `human get <BUG_KEY>` — the CLI auto-detects the owning tracker from the key shape, regardless of how many trackers are configured; `human tracker list` only enumerates trackers and must not be used to guess a key's owner. Call the tracker `<tracker>`.

If `$ARGUMENTS` contains `--board`, set `<BOARD_CONTEXT>` = true, otherwise false. `<BOARD_CONTEXT>` is the daemon's **mechanical** board signal: the container holds no push/PR credentials and a board run must stop before deploy (it runs the review inline but never pushes/opens/merges a PR). The daemon's Deploy stage owns push → PR → CI → merge on the host against the bind-mounted repo. Branch on `<BOARD_CONTEXT>` wherever the deploy path would push or ship (do not rely on the agent noticing the `HUMAN_AGENT_NAME` env var).

## Step 2 — Phase 1: Triage & reproduce (verdict)

Delegate to the **human-bug-triage** agent:

```
Task(subagent_type="human-bug-triage", prompt="Triage bug ticket <BUG_KEY>: reproduce it minimally, trace the full cause chain (symptom → proximate cause → underlying cause) with file:line evidence and the regression window, scan for sibling occurrences of the same defect pattern, and reach a verdict. Post the verdict comment on the ticket with a plain-language Explanation section a non-engineer can follow.")
```

It posts a `[human:bug-verdict] <verdict>` comment on the bug ticket — the ticket's permanent root-cause record: a plain-language explanation first, then the reproduction, the cause chain down to the underlying cause (not just the line that crashed), the regression window, and sibling occurrences. It returns the verdict (`confirmed` | `not-a-bug` | `undetermined`) plus, for a confirmed bug, the root cause and a fix outline. If the returned analysis stops at a proximate cause ("X is null" without *why* X can be null), re-dispatch the triage agent once, telling it which "why" is unanswered — do not carry a shallow root cause into the plan.

## Step 3 — Verdict gate

- **confirmed** — continue to Step 4.
- **not-a-bug** or **undetermined** — do NOT act on the verdict yet. A no-fix verdict closes or parks a ticket with no code change — the one outcome that can silently bury a real bug — so it must first survive an adversarial challenge (Step 3a).

### Step 3a — Adversarial challenge (not-a-bug / undetermined only)

Dispatch the skeptic against the verdict:

```
Task(subagent_type="human-verdict-skeptic", prompt="Challenge the latest bug-verdict on ticket <BUG_KEY>")
```

Read its `verdict-challenge:` line:

- **UPHELD** — the verdict stands; act on it:
  - **not-a-bug** — close the ticket with `human close <BUG_KEY>` (closed-type status, falling back to done-type when the workflow has none). Make **no code changes**. Post the terminal marker with `human marker post <BUG_KEY> no-fix-needed --field verdict=not-a-bug --field challenge=upheld`, then Report and STOP.
  - **undetermined** — make **no code changes**. Leave the ticket open for a human. Post the terminal marker with `human marker post <BUG_KEY> no-fix-needed --field verdict=undetermined --field challenge=upheld`, then Report and STOP.
- **REFUTED** — the bug is real after all. Post the skeptic's evidence as a confirmed verdict on `<BUG_KEY>`:

  ```bash
  human marker post <BUG_KEY> bug-verdict --head confirmed --body-file - <<'EOF'
  ## Verdict overturned on adversarial challenge
  <the skeptic's refutation: reproduction, missing commit, or contradicting output>
  EOF
  ```

  Then **continue to Step 4 as a confirmed bug**, using the skeptic's reproduction as the reproduction. Do NOT close anything, do NOT post `[human:no-fix-needed]`. The challenge runs ONCE — a refuted verdict never loops back through triage.

The `[human:no-fix-needed]` marker is **mandatory in board context**: the autofix pipeline runs under the board implementation-stage agent name, whose failure watcher treats any exit with no `[human:ready-for-review]` handoff as a crash and would loop forever re-triaging. This terminal marker signals the clean, resolved stop (ticket 405). The `human marker post` call above renders:

```
[human:no-fix-needed]
verdict: <not-a-bug | undetermined>
challenge: upheld
```

## Step 4 — Phase 2: Plan (topology decides where it lives)

1. Resolve the topology with `human tracker topology` — it returns `{"topology":"single"|"split","pm":{...},"engineering":{...}}` (`engineering` omitted in single mode).
   - **Split topology** — note the engineering tracker's name and its first project (e.g. Linear project `HUM`) as `<ENG_TRACKER>` and `<ENG_PROJECT>`. The plan becomes a separate engineering ticket.
   - **Single-tracker topology** — the plan becomes a `[human:plan]` comment on the bug ticket itself; no second ticket.
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
human marker post <BUG_KEY> plan --body-file - <<'PLAN_EOF'
<PLAN_CONTENT>
PLAN_EOF
```

Verify with `human plan show <BUG_KEY>` — the fixer and verify agents read the plan the same way. Commits reference only `<BUG_KEY>`. Set `<WORK_KEY>` to `<BUG_KEY>`.

## Step 5 — Phase 3: Test-first fix

Delegate to the **human-bug-fixer** agent. When `<BOARD_CONTEXT>` is true the fixer must NOT push — the board container has no push credentials and Deploy owns shipping — so forward the board instruction explicitly in the dispatch prompt (the fixer cannot see `$ARGUMENTS`):

```
Task(subagent_type="human-bug-fixer", prompt="Fix ticket <WORK_KEY> (PM bug <BUG_KEY>) test-first on a feature branch. BOARD CONTEXT: do NOT run git push — leave the branch local; the daemon's Deploy stage ships it. Report the local branch name. Iterate on the fast test+lint tier (not the full `make check`) to go green — the verify gate runs the single full suite.")
```

Otherwise (standalone, `<BOARD_CONTEXT>` false) dispatch the existing push prompt:

```
Task(subagent_type="human-bug-fixer", prompt="Fix ticket <WORK_KEY> (PM bug <BUG_KEY>) test-first on a feature branch and push it. Iterate on the fast test+lint tier (not the full `make check`) to go green — the verify gate runs the single full suite.")
```

It creates branch `autofix/<work-key>` (the key lowercased), writes a regression test that **fails** because of the bug, implements the root-cause fix, confirms the suite is green, commits with subjects starting with the `human commits prefix <BUG_KEY> [<ENG_KEY>]` prefix (e.g. `[SC-79] [HUM-59]` in split topology, `[SC-79]` otherwise), and returns the branch name. In a standalone run it pushes the branch; in board context it leaves the branch local (the bind-mounted host repo) and returns its name without pushing. If it reports it could not reach a green build/test, STOP and report — do not open a PR.

## Step 6 — Phase 4: Verify (done gate)

Delegate to the **human-bug-verify** agent:

```
Task(subagent_type="human-bug-verify", prompt="Verify ticket <WORK_KEY> (PM bug <BUG_KEY>): confirm the regression test fails before / passes after the fix, the full suite is green, and the fix addresses the root cause. Post the verdict as a comment on <BUG_KEY>.")
```

This is the pipeline's ONE full-suite pass; the fixer used the fast tier. Ensure the `[human:bug-verify]` comment records the `## Evidence` block (branch/commit/command/result) so the review can trust it without re-running the suite.

If the verdict is NOT DONE, re-run Step 5 once to address the gaps; if it still fails, do NOT stop silently — in board context a silent stop freezes the card at "being fixed" forever with no agent and no reconciliation path (1136). Before stopping, post an explicit terminal marker so the board reds the card to a needs-attention/Retry badge instead:

```bash
human marker post <BUG_KEY> implementation-failed --body-file - <<'EOF'
<one-line verdict headline — becomes the card's badge text>

<the bug-verify gaps: what is still NOT DONE and why>
EOF
```

The first body line becomes the badge headline. This is mandatory in board context — the bug-fix analog of the no-dead-end-states work (SC-355/591). Then STOP and report honestly without posting the handoff.

## Step 7 — Phase 5: Hand off and review

Only after a DONE verdict.

### 7.1 Post the review handoff

Post the review handoff on the bug (PM) ticket — the **same handoff the kanban executor posts**, so the trail and the board's `(R)` annotation work identically:

```bash
human handoff post <BUG_KEY> --engineering <ENG_KEY> --branch autofix/<work-key>   # split topology
human handoff post <BUG_KEY> --branch autofix/<work-key>                           # single-tracker: omit --engineering
```

The explicit `--branch` pins the fix branch even when the orchestrating checkout sits elsewhere. The command derives the rest — `commits:` from the commits referencing `<WORK_KEY>`, `daemon:` from the `HUMAN_DAEMON_ID` env var so the handoff is attributed to the machine's bot like every daemon-posted marker (SC-660 rule 1; the line is omitted when the var is unset) — then verifies every SHA is reachable on the branch (fetching origin first) and refuses to post otherwise, so a handoff can never name commits that live nowhere. The posted comment looks like:

```
[human:ready-for-review]
engineering: <ENG_KEY>
branch: autofix/<work-key>
commits: <short-shas>
daemon: <daemon-id>
```

When `<BOARD_CONTEXT>` is true the branch is intentionally local (the bind-mounted host repo where Deploy picks it up) — do NOT push. If the handoff cannot be posted (non-zero exit), STOP with an honest status report — **do not report success**.

**Board-context exception applies here**: when `<BOARD_CONTEXT>` is true, post the handoff (so `branch:`/`commits:` are recorded for the Deploy button), then CONTINUE to the inline review (Steps 7.2–7.3) in this same warm container. STOP after the review (do not run Step 8 / deploy, which needs credentials the board container lacks). Do NOT run push-verification and do NOT `git ls-remote` — the branch is intentionally local. The daemon recognizes the in-container `[human:review-complete]` marker and does NOT launch a second review container; the Deploy button ships the reviewed fix.

### 7.2 Review by the reviewer agent

Chain straight into the review, like the kanban flow chains a clean build. This runs **inline in this same warm container in board context too** (SC-782) — it is no longer skipped when `<BOARD_CONTEXT>` is true; only Step 8 (deploy) is. Post the started marker, then dispatch the reviewer:

```bash
human marker post <BUG_KEY> review-started
```

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket <WORK_KEY>: check out branch autofix/<work-key> and review its diff against main against the ticket's plan and acceptance criteria.")
```

The reviewer writes `.human/reviews/<work-key>.md`; the first line under its `## Summary` is the outcome — `pass`, `pass with notes`, `fail`, or `unreviewable: <reason>` (the code could not be obtained — e.g. the branch is unreachable or no commits reference the key). Post the outcome on the bug ticket (same follow-up the review pickup flow posts). The `[human:review-complete]` comment below is only for reviews that examined code; an `unreviewable` outcome is handled by the 7.3 gate instead. The comment is the canonical record: inline the reviewer's **full findings** under a `## Findings` section so the board detail panel shows what was found without opening the local `.human/reviews/<work-key>.md` (which stays a working artifact):

```bash
human marker post <BUG_KEY> review-complete \
  --field verdict="<verdict>" \
  --field reviews="<WORK_KEY>: <verdict> — .human/reviews/<work-key>.md" \
  --body-file - <<'REVIEW_EOF'
## Findings
<the reviewer's full findings, inlined: what was checked, every issue found
 (or "no issues"), and any notes — the substance of .human/reviews/<work-key>.md,
 not just a pointer to it>
REVIEW_EOF
```

### 7.3 Review gate

- **pass** or **pass with notes** — continue to Step 8.
- **unreviewable** — the reviewer could not obtain the code, so there are NO findings. Do NOT re-dispatch the **human-bug-fixer** and do NOT post `[human:review-complete] verdict: fail` (that would badge the card "review found problems" and point a rework run at phantom findings). Instead post `[human:review-failed]` on the bug ticket naming the unreachable ref — `human marker post <BUG_KEY> review-failed --field reason="<reachability reason>"` — then STOP (report per Step 9). No PR is merged. The card shows an honest, retryable stage failure. The board-context 7.1 stop is unchanged.
- **fail** — feed the reviewer's findings back once: re-dispatch the **human-bug-fixer** (Step 5) with the review findings appended to the prompt, re-run the verify gate (Step 6), then re-run the review (7.2, one new `[human:review-complete]` comment). If the second verdict still fails, STOP honestly: the `[human:ready-for-review]` handoff stays standing for a human, and NO pull request is merged.

## Step 8 — Phase 6: Deploy — end with a merged PR

Only after a passing review. This is the board's deploy pipeline (push → PR → CI gate → merge → close) driven from the skill:

1. Post the started marker: `human marker post <BUG_KEY> deploy-started` (`human deploy` does not post this itself).
2. Run the deploy gate:

   ```bash
   human deploy <BUG_KEY> --branch autofix/<work-key> --title "[<BUG_KEY>] [<ENG_KEY>] <short summary>"
   ```

   (single-tracker: only `[<BUG_KEY>]` in the title; `--branch` defaults to the ticket's newest review-handoff branch and `--title` to the ticket title). The command owns the whole gate: push + PR, the CI wait (blocks up to 45 minutes), rebase-if-stale with a lease push, merge, remote-branch cleanup, the `[human:deployed]` marker with its `pr:` line, and the ticket close. A branch already merged into the base is a clean success. It runs a recovery ladder internally: a racy merge refusal (the PR is mergeable but the forge is still reconciling fresh checks) is waited out and retried, and it only posts `[human:deploy-failed]` — with the specific unresolved blocker named — once that ladder is exhausted, exiting non-zero. A `[human:deploy-failed]` is therefore an honest needs-human end state, not a first-failure stop: do NOT merge by hand and do NOT re-implement the already-reviewed work; the PR stays open for a human with the named blocker. The one thing you must never do is end the run with the card in a non-terminal state and no live agent — the only acceptable ends are deployed/closed or a `[human:deploy-failed]` naming the blocker.
3. In split topology, close `<ENG_KEY>` as well: `human done <ENG_KEY>`.
4. For the Step 9 report, read `<PR_URL>` from the deployed marker if needed: `human marker show <BUG_KEY> deployed`.

## Step 9 — Run summary: ticket comment, then report

Once a fix was attempted (Step 4 ran), the ticket must carry a plain-language account of the run — a person catching up later should not have to reconstruct it from markers and agent artifacts. Post it at EVERY terminal point after Step 4: the board-context stop after the handoff (7.1), a shipped fix (Step 8), and every honest STOP (fixer could not go green, verify not DONE, review failed twice, deploy gate red). Runs that end at the verdict gate (Step 3) post nothing here — the triage verdict comment already tells that story.

```bash
human marker post <BUG_KEY> fix-summary --body-file - <<'SUMMARY_EOF'
## What happened
<2–4 sentences, plain language: what the bug turned out to be and what the fix does. Written for the reporter, not an engineer.>

## Changes
- Branch: autofix/<work-key> — <left local for Deploy | pushed | merged as <PR_URL>>
- Commits: <short sha — one-line subject, per commit>
- <the areas of the product touched, one line>

## Proof
- Regression test: <name/location> — failed before the fix, passes after
- Checks: <suite/lint/coverage result>
- Review: <verdict, or "pending — daemon chains it" in board context>

## Along the way
<the story of the run when it was not straight: a re-dispatched triage, a first verify that came back not-DONE, review findings that were addressed, infrastructure trouble. If the run went straight through, say exactly that: "Nothing notable — triage, fix, verify, and review went through on the first pass.">

## Where it ended
<board: handoff posted, the Deploy button ships it | standalone: PR merged, ticket closed by the deploy gate | stopped at <step>: what a human needs to do next>
SUMMARY_EOF
```

Fill every section from what actually happened in THIS run — never leave template placeholders in the posted comment. If posting the summary fails, still produce the final report below.

Then report the verdict. For a confirmed, shipped fix, present the traceability chain:

```
Autofix complete for <BUG_KEY>

Verdict: confirmed — review: <verdict> — shipped
- PM bug:     <tracker> <BUG_KEY>
- Root cause: [human:bug-verdict] comment on <BUG_KEY> (explanation + cause chain)
- Plan:       <ENG_TRACKER> <ENG_KEY> (split topology) — or [human:plan] comment on <BUG_KEY>
- Branch:     autofix/<work-key>
- Review:     [human:review-complete] verdict: <verdict> on <BUG_KEY>
- PR:         <PR_URL> — merged, branch deleted
- Ticket:     closed by the deploy gate (`human deploy`)
```

For a board-context run (exception in Step 7.1) or a failed review/deploy gate, report where the pipeline stopped, which marker records it, and what a human needs to do next.
