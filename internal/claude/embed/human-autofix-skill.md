---
name: human-autofix
description: Autonomously verify, reproduce, root-cause, fix, review, and ship a reported bug end to end — a passing review ends in a merged PR
argument-hint: <bug-ticket-key>
---

# Overview

Point this skill at a bug ticket and it runs the full bug-fix pipeline autonomously: **triage & reproduce → root-cause explanation on the ticket → verdict → (if a real bug) plan → test-first fix on a branch → verify → review by the reviewer agent → (on a passing review) deploy: PR → CI gate → merge**. The whole trail is recorded on the tracker (comments + the plan — a separate engineering ticket in split topology, a `[human:plan]` comment on the bug ticket itself otherwise), and every run that attempted a fix ends by posting a plain-language `[human:fix-summary]` comment on the ticket (Step 9); the only `.human/` working file is the reviewer's `.human/reviews/<key>.md`.

The run does **not** end at the review handoff: exactly like the kanban flow — where a clean build chains straight into its review and Deploy ships it — the skill chains the fix into a review by the **human-reviewer** agent and, when the verdict is a pass, drives the same deploy pipeline the board's Deploy stage runs (push → PR → CI gate → merge → close). A failing review or a red CI gate stops the run honestly with the handoff left standing for a human.

**Board-context exception**: when `<BOARD_CONTEXT>` is true (launched with `--board`; `HUMAN_AGENT_NAME` starting with `board-` is a fallback signal), this skill runs *as a board stage agent*. The container holds no push/PR credentials and the Bugs pane's Deploy button owns shipping, so **end after the review (Step 7.3) and skip Step 8 (deploy) entirely**. The review itself now runs **inline, in this warm container** (Steps 7.2–7.3) — same workspace and caches the fix was built in — so a bug pays **one** container startup, not two, instead of a second container spun up later just to run the review. Do NOT push, open, or merge a PR in board context.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — reach a verdict and act on it; the pipeline is required to run end to end with no further input. Every run ends in exactly one verdict: **confirmed**, **not-a-bug**, or **undetermined**.

Follow these steps in order.

## Retry budgets and flakes

A stage that fails does **not** end the run on the first failure. Before charging an attempt, establish that the failure is real:

1. Re-run the failing test or check **alone**. If it passes in isolation it is a **flake**: record it and do not charge the attempt.
2. Only a failure that reproduces identically twice counts as real.
3. Charge a real failure against the stage's budget, then retry until the budget is spent.

The budget is **3 real attempts per stage**, tracked in agent state so it survives a stage handoff or a container restart:

```bash
human state incr <BUG_KEY> budget.<stage>.flakes     # a failure that vanished in isolation
human state incr <BUG_KEY> budget.<stage>.attempts   # a failure that reproduced
human state get  <BUG_KEY> budget.<stage>.attempts --default 0
```

An infrastructure failure — a dead container, a network blip, a runner that never started — is never a real attempt. It is a `retryable` exit (see the exit contract below); say so instead of burning the budget on it.

Exhausting the budget is an honest `needs-human-work` ending, not a silent stop: post the terminal marker the step calls for and report what the three attempts each tried and why each failed.

## Recording the board stage outcome

In board context this run **is** the implementation stage, and the daemon reads that stage's exit from agent state key `stage.implementation` (the board stage name it looks up — distinct from the per-phase `stage.triage`/`stage.verify`/`stage.review` records this skill already writes). A run that stops without writing it hands the daemon nothing but the generic "agent finished without posting the stage handoff" diagnosis, and the retry loop charges on blind.

So: **a board-context run must never exit without writing `stage.implementation`.** At every point where the run STOPS in board context — the budget-spent stop (Step 6), a `DECISION REQUIRED` options post (Step 1a), an unreviewable or twice-failed review (Step 7.3), and the clean no-fix-needed terminal (Step 3a) — record the outcome in the stage's own terms *before* returning, alongside the marker that stop already posts:

```bash
human state set <BUG_KEY> stage.implementation --json --body-file - <<'EOF'
{"exit":"needs-human-work",
 "summary":"one line in the stage's own terms — e.g. verify budget spent after 3 real attempts; gaps: <…>",
 "evidence":"the marker just posted (e.g. [human:implementation-failed]) and the state keys that back it",
 "unchecked":"<dependent kinds this run could not determine, and why — empty if none>",
 "next":"what a human must decide or do"}
EOF
```

Use the exit vocabulary the board understands (`internal/daemon/board_retry.go`): `retryable`, `outage`, `needs-input`, `needs-human-work`, `done`. A clean resolved terminal (no-fix-needed, Step 3a) records `{"exit":"done", ...}` alongside its `[human:no-fix-needed]` marker; a spent budget records `needs-human-work`; an interrupted-substrate stop records `retryable`/`outage`. This record is additive — it does not replace the phase records.

<!-- human:include dependents -->

<!-- human:include exit-contract -->

<!-- human:include model-tiers -->

The tiers this pipeline uses, unless you have a reason to differ: triage, planner, reviewer and every adversarial check at `opus`; bug-fixer and bug-verify at `sonnet`; preflight inherits.

## Step 1 — Parse argument

`$ARGUMENTS` is the bug ticket key — the PM ticket — optionally followed by `--board`. Take the first non-flag token as `<BUG_KEY>`. Resolve the bug ticket with `human get <BUG_KEY>` — the CLI auto-detects the owning tracker from the key shape, regardless of how many trackers are configured; `human tracker list` only enumerates trackers and must not be used to guess a key's owner. Call the tracker `<tracker>`.

Then take ownership: `human assign <BUG_KEY>`. Ownership records who is working the ticket; it sets the owner only, never the status, so it cannot block on an approval gate. In board context the daemon already took ownership at launch and this is a harmless no-op. A failure here never stops the run — note it and continue.

### Step 1a — Preflight (ask once, up front, or not at all)

Before any work, run preflight. It resolves what this run may do, settles what the evidence can settle, and surfaces a decision only a human can make **now** rather than halfway through:

```
Task(subagent_type="human-preflight", prompt="Preflight bug ticket <BUG_KEY> before an autonomous fix run: resolve capabilities, mirror decisions already made, and surface any genuine product/scope fork as a DECISION REQUIRED terminal.", run_in_background=false)
```

Read its outcome from state:

```bash
human state get <BUG_KEY> stage.preflight --field ready       # yes | no
human state get <BUG_KEY> stage.preflight --field question    # the fork, when ready is "no"
```

- **ready: yes** — the capability set and any prior decisions are recorded; continue to Step 2.
- **ready: no** — preflight returned a `DECISION REQUIRED:` terminal. Surface it as the **existing** up-front decision block and STOP; do not triage, plan, or fix into an unmade decision:

  ```bash
  human marker post <BUG_KEY> options \
    --field stage=implementation \
    --field context="<the DECISION REQUIRED one-liner>" \
    --field 1="<first option>" \
    --field 2="<second option>"
  ```

  Pass through every `waits-for-<id>:` line preflight emitted, as a field of the same name (`--field waits-for-1=<KEY>`). That line is what makes an answer meaning *"<KEY> goes first"* hold this ticket instead of starting it — dropping it turns the answer into its opposite.

  The board renders this as "Decision needed" and the card waits without being mistaken for a crash. When the human picks, the daemon records `[human:option-chosen]` and relaunches this run with the choice in hand; preflight then mirrors it into `decisions` and returns `ready: yes`. An answer that declared a wait is the exception: the daemon records it and starts nothing, and the relaunch comes later, once the ticket it deferred to is done. Do **not** invent a `needs-input` marker — this loop already exists and a second one would split the trail. In board context, alongside the options post, record the stage outcome (`stage.implementation`, exit `needs-input`, per "Recording the board stage outcome") so the daemon reads the awaited decision rather than the generic diagnose line.

Preflight records the capability set, so it is available to every later stage:

```bash
human state get <BUG_KEY> capabilities --field can_push    # false in board context
human capabilities                                          # the same answer, human-readable
```

The capability set is the single source of truth for the rest of the run — do not infer permissions from flags or env vars:

```json
{"board_context": true, "can_push": false, "can_open_pr": false, "owns_deploy": false, "workspace": "bind-mounted"}
```

**The rule is one line: attempt nothing the capability set forbids, and treat a missing capability as a boundary, never as a failure.** A run that cannot push has not failed to push; pushing was simply never its job.

Set `<BOARD_CONTEXT>` to the set's `board_context`. (`--board` in `$ARGUMENTS` is the daemon's explicit signal and still forces it true; the capability set detects it independently from the `board-…` agent name, so the two agree even when the flag is missing.) In board context the container holds no push/PR credentials and the daemon's Deploy stage owns push → PR → CI → merge on the host against the bind-mounted repo: the run stops before deploy, having run the review inline.

### Step 1b — Resume from the furthest recorded step

This run may be a **recovery relaunch** of one that was interrupted mid-pipeline — a container that died, a daemon that restarted. A resumed run picks up where the account on the ticket stops; it does not start over, and a step that is already recorded is not paid for again. Read the ticket's markers once, then resume from the furthest recorded step:

```bash
human plan show <BUG_KEY>        # non-empty -> a [human:plan] comment exists; the plan is done
# a [human:bug-verdict] comment present -> triage is done
```

- **A `[human:bug-verdict]` comment already exists → skip Step 2 (triage).** The cause is recorded. Read the verdict and root cause from `stage.triage` state (`human state get <BUG_KEY> stage.triage --field verdict`); if that state record is missing but the marker is present, read them from the marker body (`human marker show <BUG_KEY> bug-verdict`). Go straight to the verdict gate (Step 3). Do **not** re-run triage.
- **A `[human:plan]` comment already exists → skip Step 4 (plan).** The plan is recorded; proceed to Step 5 (fix) using it. Re-planning is only for a genuinely absent plan.
- **A missing later step is this run's own work, never a blocker.** This pipeline writes its own plan: **never** post `[human:needs-planning]` and **never** ask a human to run planning. If the plan is absent and triage confirmed the bug, produce the plan yourself (Step 4) and continue.

## Step 2 — Phase 1: Triage & reproduce (verdict)

Delegate to the **human-bug-triage** agent:

```
Task(subagent_type="human-bug-triage", model="opus", prompt="Triage bug ticket <BUG_KEY>: reproduce it minimally, trace the full cause chain (symptom → proximate cause → underlying cause) with file:line evidence and the regression window, scan for sibling occurrences of the same defect pattern, and reach a verdict. Post the verdict comment on the ticket with a plain-language Explanation section a non-engineer can follow.", run_in_background=false)
```

It posts a `[human:bug-verdict] <verdict>` comment on the bug ticket — the ticket's permanent root-cause record: a plain-language explanation first, then the reproduction, the cause chain down to the underlying cause (not just the line that crashed), the regression window, and sibling occurrences. **Read the verdict from state, not from the agent's prose:**

```bash
human state get <BUG_KEY> stage.triage --field verdict     # confirmed | not-a-bug | undetermined
human state get <BUG_KEY> stage.triage --field root_cause
```

The agent records `stage.triage` before returning (per the exit contract). Its message is for a human reader; the state record is what you branch on — a rephrased summary must never change the routing. If `stage.triage` is missing, the stage did not complete: treat that as `retryable` and re-dispatch rather than guessing a verdict from the text — **at most twice**. A record that is still missing after two re-dispatches is not a flaky agent, it is a broken state store (most often a daemon that predates `human state`). Stop then with `needs-human-work`, naming state as the suspect, instead of re-dispatching forever:

```bash
human state incr <BUG_KEY> budget.triage.missing              # count this miss
human state get  <BUG_KEY> budget.triage.missing --default 0  # at 3, stop
```

Increment, then read it back and compare — a counter that is only ever incremented bounds nothing.

If `human state` itself errors, do not loop on it either — the same two-attempt bound applies to every stage record this skill reads.

For a confirmed bug the record also carries the root cause and fix outline. If the recorded analysis stops at a proximate cause ("X is null" without *why* X can be null), re-dispatch the triage agent once, telling it which "why" is unanswered — do not carry a shallow root cause into the plan.

## Step 3 — Verdict gate

- **confirmed** — continue to Step 4.
- **not-a-bug** or **undetermined** — do NOT act on the verdict yet. A no-fix verdict closes or parks a ticket with no code change — the one outcome that can silently bury a real bug — so it must first survive an adversarial challenge (Step 3a).

### Step 3a — Adversarial challenge (not-a-bug / undetermined only)

Dispatch the skeptic against the verdict:

```
Task(subagent_type="human-verdict-skeptic", model="opus", prompt="Challenge the latest bug-verdict on ticket <BUG_KEY>", run_in_background=false)
```

Read its outcome from state:

```bash
human state get <BUG_KEY> stage.challenge --field challenge   # upheld | refuted
```

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

The `[human:no-fix-needed]` marker is **mandatory in board context**: the autofix pipeline runs under the board implementation-stage agent name, whose failure watcher treats any exit with no `[human:ready-for-review]` handoff as a crash and would loop forever re-triaging. This terminal marker signals the clean, resolved stop (ticket 405). Alongside it, record the stage outcome (`stage.implementation`, exit `done`, per "Recording the board stage outcome") so the daemon reads a resolved terminal rather than the generic diagnose line. The `human marker post` call above renders:

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
Task(subagent_type="human-planner", model="opus", prompt="Create an implementation plan to fix bug <BUG_KEY>. Decisions already settled for this ticket (do not re-open any of them): <paste the output of `human state get <BUG_KEY> decisions --default '{}'`>. The root-cause analysis from triage:\n<paste the triage root cause + fix outline>\nThe plan's Changes section MUST begin with adding a regression test that fails because of the bug, then fixing the root cause. Return the plan as output; do not write files or create tickets.", run_in_background=false)
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
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="Fix ticket <WORK_KEY> (PM bug <BUG_KEY>) test-first on a feature branch. BOARD CONTEXT: do NOT run git push — leave the branch local; the daemon's Deploy stage ships it. Report the local branch name. Iterate on the fast test+lint tier (not the full `make check`) to go green — the verify gate runs the single full suite.", run_in_background=false)
```

Otherwise (standalone, `<BOARD_CONTEXT>` false) dispatch the existing push prompt:

```
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="Fix ticket <WORK_KEY> (PM bug <BUG_KEY>) test-first on a feature branch and push it. Iterate on the fast test+lint tier (not the full `make check`) to go green — the verify gate runs the single full suite.", run_in_background=false)
```

It creates branch `autofix/<work-key>` (the key lowercased), writes a regression test that **fails** because of the bug, implements the root-cause fix, confirms the suite is green, commits with subjects starting with the `human commits prefix <BUG_KEY> [<ENG_KEY>]` prefix (e.g. `[<PM_KEY>] [<ENG_KEY>]` in split topology, `[<PM_KEY>]` otherwise), and returns the branch name. In a standalone run it pushes the branch; in board context it leaves the branch local (the bind-mounted host repo) and returns its name without pushing. If it reports it could not reach a green build/test, STOP and report — do not open a PR.

## Step 6 — Phase 4: Verify (done gate)

Delegate to the **human-bug-verify** agent:

```
Task(subagent_type="human-bug-verify", model="sonnet", prompt="Verify ticket <WORK_KEY> (PM bug <BUG_KEY>): confirm the regression test fails before / passes after the fix, the full suite is green, and the fix addresses the root cause. Post the verdict as a comment on <BUG_KEY>.", run_in_background=false)
```

This is the pipeline's ONE full-suite pass; the fixer used the fast tier. Ensure the `[human:bug-verify]` comment records the `## Evidence` block (branch/commit/command/result) so the review can trust it without re-running the suite.

**Read the gate's outcome from state:**

```bash
human state get <WORK_KEY> stage.verify --field verdict   # DONE | NOT DONE
human state get <WORK_KEY> stage.verify --field gaps      # what is still missing, when NOT DONE
```

If the verdict is NOT DONE, re-run Step 5 to address the gaps, under the retry budget above — charge an attempt only for a failure that reproduced, and keep going while the budget holds. Once the budget is spent, do NOT stop silently — in board context a silent stop freezes the card at "being fixed" forever with no agent and no reconciliation path (1136). Before stopping, post an explicit terminal marker so the board reds the card to a needs-attention/Retry badge instead:

```bash
human marker post <BUG_KEY> implementation-failed --body-file - <<'EOF'
<one-line verdict headline — becomes the card's badge text>

<the bug-verify gaps: what is still NOT DONE and why>
EOF
```

The first body line becomes the badge headline. This is mandatory in board context — every run must leave a visible, honest outcome behind rather than a card left silently "running". Then record the stage outcome (`stage.implementation`, exit `needs-human-work`, per "Recording the board stage outcome") so the daemon reads the spent budget instead of the generic diagnose line, and STOP and report honestly without posting the handoff.

## Step 7 — Phase 5: Hand off and review

Only after a DONE verdict.

### 7.1 Post the review handoff

Post the review handoff on the bug (PM) ticket — the **same handoff the kanban executor posts**, so the trail and the board's `(R)` annotation work identically:

```bash
human handoff post <BUG_KEY> --engineering <ENG_KEY> --branch autofix/<work-key>   # split topology
human handoff post <BUG_KEY> --branch autofix/<work-key>                           # single-tracker: omit --engineering
```

The explicit `--branch` pins the fix branch even when the orchestrating checkout sits elsewhere. The command derives the rest — `commits:` from the commits referencing `<WORK_KEY>`, `daemon:` from the `HUMAN_DAEMON_ID` env var so the handoff is attributed to the machine's bot like every daemon-posted marker (the line is omitted when the var is unset) — then verifies every SHA is reachable on the branch (fetching origin first) and refuses to post otherwise, so a handoff can never name commits that live nowhere. The posted comment looks like:

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

Chain straight into the review, like the kanban flow chains a clean build. This runs **inline in this same warm container in board context too** — it is no longer skipped when `<BOARD_CONTEXT>` is true; only Step 8 (deploy) is. Post the started marker, then dispatch the reviewer:

```bash
human marker post <BUG_KEY> review-started
```

```
Task(subagent_type="human-reviewer", model="opus", prompt="Review changes for ticket <WORK_KEY>: check out branch autofix/<work-key> and review its diff against main against the ticket's plan and acceptance criteria.", run_in_background=false)
```

The reviewer writes `.human/reviews/<work-key>.md` and records its outcome in state. **Read the verdict from state, never from the file's prose:**

```bash
human state get <WORK_KEY> stage.review --field verdict   # pass | pass with notes | fail | incomplete | unreviewable
human state get <WORK_KEY> stage.review --field reason    # why, when unreviewable
human state get <WORK_KEY> stage.review --field unchecked  # dependent kinds the reviewer could not determine
```

A non-empty `unchecked` never changes the verdict routing — carry it into the run summary's "Along the way" so a kind nobody could query is part of the story of the run rather than a silence.

The five verdicts mean: the change is good (`pass`), good with notes worth recording (`pass with notes`), it has problems to fix (`fail`), it was built correctly but not every ticket acceptance criterion was met (`incomplete`), or the code could not be obtained at all — the branch is unreachable or no commits reference the key (`unreviewable`). Post the outcome on the bug ticket (same follow-up the review pickup flow posts). The `[human:review-complete]` comment below is only for reviews that examined code; an `unreviewable` outcome is handled by the 7.3 gate instead. The comment is the canonical record: inline the reviewer's **full findings** under a `## Findings` section so the board detail panel shows what was found without opening the local `.human/reviews/<work-key>.md` (which stays a working artifact):

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

- **pass** or **pass with notes** — a pass is the one review outcome nothing downstream checks, and it is about to be made irreversible by a merge. Before continuing, get one adversarial second opinion:

  ```
  Task(subagent_type="human-second-opinion", model="opus", prompt="The pipeline is about to merge branch autofix/<work-key> for ticket <WORK_KEY> on the strength of a passing review. Lens: did-you-actually-look. Evidence: the ticket, the branch diff against main, and stage.review in agent state. Try to refute that the review examined the change. Do not read the reviewer's reasoning first.", run_in_background=false)
  ```

  ```bash
  human state get <WORK_KEY> stage.opinion --field opinion    # upheld | refuted
  ```

  - **upheld** — continue to Step 8.
  - **refuted** — treat it exactly like a failing review: feed its evidence back to the fixer under the review budget (the `fail` branch below). Do not merge on a refuted pass.

  Run this once per review verdict, not once per attempt: a second opinion on the same unchanged code twice is noise.
- **unreviewable** — the reviewer could not obtain the code, so there are NO findings. Do NOT re-dispatch the **human-bug-fixer** and do NOT post `[human:review-complete] verdict: fail` (that would badge the card "review found problems" and point a rework run at phantom findings). Instead post `[human:review-failed]` on the bug ticket naming the unreachable ref — `human marker post <BUG_KEY> review-failed --field reason="<reachability reason>"` — then record the stage outcome (`stage.implementation`, exit `retryable`, per "Recording the board stage outcome") and STOP (report per Step 9). No PR is merged. The card shows an honest, retryable stage failure. The board-context 7.1 stop is unchanged.
- **fail** or **incomplete** — feed the reviewer's findings back: re-dispatch the **human-bug-fixer** (Step 5) with the review findings appended to the prompt, re-run the verify gate (Step 6), then re-run the review (7.2, one new `[human:review-complete]` comment). An `incomplete` verdict means a ticket acceptance criterion was not built; route it identically to `fail` — re-dispatch the fixer with the unmet criterion appended, re-verify, and re-review under the same `budget.review.attempts`. This loops under the retry budget (`budget.review.attempts`) — a review that fails for a *different* reason each round is progress, while the same finding surviving twice is not. When the budget is spent, STOP honestly as `needs-human-work`: the `[human:ready-for-review]` handoff stays standing for a human, and NO pull request is merged.

## Step 8 — Phase 6: Deploy — end with a merged PR

Only after a passing review. This is the board's deploy pipeline (push → PR → CI gate → merge → close) driven from the skill:

1. Run the deploy gate:

   ```bash
   human deploy <BUG_KEY> --branch autofix/<work-key> --title "[<BUG_KEY>] [<ENG_KEY>] <short summary>"
   ```

   (single-tracker: only `[<BUG_KEY>]` in the title; `--branch` defaults to the ticket's newest review-handoff branch and `--title` to the ticket title). The command owns the whole gate: push + PR, the CI wait (blocks up to 45 minutes), rebase-if-stale with a lease push, merge, remote-branch cleanup, the `[human:deployed]` marker with its `pr:` line, and the ticket close. A branch already merged into the base is a clean success. It runs a recovery ladder internally: a racy merge refusal (the PR is mergeable but the forge is still reconciling fresh checks) is waited out and retried, and it only posts `[human:deploy-failed]` — with the specific unresolved blocker named — once that ladder is exhausted, exiting non-zero. A `[human:deploy-failed]` is therefore an honest needs-human end state, not a first-failure stop: do NOT merge by hand and do NOT re-implement the already-reviewed work; the PR stays open for a human with the named blocker. The one thing you must never do is end the run with the card in a non-terminal state and no live agent — the only acceptable ends are (a) deployed/closed, (b) a `[human:deploy-failed]` naming the blocker, or (c) a deploy refused because an open `[human:options]` decision is waiting: report it as `needs-input` and leave the card paused — it is neither a failure nor a card to force.

   `human deploy` records the start on the ticket itself (`[human:deploy-started]`) before it touches the forge — do **not** post that marker by hand.

   One outcome is neither success nor failure: if the command exits with **`deploy refused: this ticket is waiting on a decision`**, an open `[human:options]` block is waiting on a person. That is not a crash and not a deploy failure — no `[human:deploy-failed]` is posted and the card is not red. Do **not** re-run with `--override-decision` (only a person may decide to ship past their own open question) and do **not** merge by hand. Post the Step 9 run summary, record the stage outcome as `needs-input` (per "Recording the board stage outcome"), and STOP, leaving the card paused where it is.
2. In split topology, close `<ENG_KEY>` as well: `human done <ENG_KEY>`.
3. For the Step 9 report, read `<PR_URL>` from the deployed marker if needed: `human marker show <BUG_KEY> deployed`.

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

## Dependents
- <the fixer's dispositions, verbatim: one examined-and-unchanged / examined-and-changed line per dependent>
- <unchecked: kind — why nobody could query it, if any (from stage.review --field unchecked and the fixer's report)>

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
