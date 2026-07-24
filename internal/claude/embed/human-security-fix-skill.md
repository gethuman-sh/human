---
name: human-security-fix
description: Autonomously confirm, threat-model, root-cause, fix, security-review, and ship a reported vulnerability end to end — a passing review ends in a merged PR
argument-hint: <security-ticket-key>
---

# Overview

Point this skill at a security ticket and it runs the full security-fix pipeline autonomously: **triage & threat-model → root-cause explanation on the ticket → verdict → (if a real vulnerability) plan → test-first fix on a branch → verify the exploit is closed → security review → (on a passing review) deploy: PR → CI gate → merge**. The whole trail is recorded on the tracker (comments + the plan — a separate engineering ticket in split topology, a `[human:plan]` comment on the security ticket itself otherwise), and every run that attempted a fix ends by posting a plain-language `[human:fix-summary]` comment on the ticket (Step 9).

This is the security counterpart to **human-autofix**: it shares the same pipeline, markers, and board plumbing (so the Bugs & Security view tracks a security fix exactly like a bug fix), but its triage builds a **threat model** and rates **severity**, its verify bar is "**the attack no longer works**", and its review adds a **security lens**. Where this document says the flow is "as in autofix", it is deliberately identical — only the security-specific stages differ.

The run does **not** end at the review handoff: exactly like the bug flow, the skill chains the fix into a review and, on a passing verdict, drives the same deploy pipeline (push → PR → CI gate → merge → close). A failing review or a red CI gate stops the run honestly with the handoff left standing for a human.

**Board-context exception**: when `<BOARD_CONTEXT>` is true (launched with `--board`; `HUMAN_AGENT_NAME` starting with `board-` is a fallback signal), this skill runs *as a board stage agent*. The container holds no push/PR credentials and the Deploy button owns shipping, so **end after the review (Step 7.3) and skip Step 8 (deploy) entirely**. The review runs **inline, in this warm container** (Steps 7.2–7.3) — one container startup, not two (SC-782). Do NOT push, open, or merge a PR in board context.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — reach a verdict and act on it. Every run ends in exactly one verdict: **confirmed**, **not-a-bug** (not a real vulnerability), or **undetermined**.

**Handle exploit detail discreetly**: the commit log is public. Keep reproduction and exploit specifics on the tracker (comments), never in commit messages — a fix commit references the ticket and describes the hardening, not how to attack an unpatched install.

Follow these steps in order.

## Retry budgets and flakes

A stage that fails does **not** end the run on the first failure. Before charging an attempt, establish the failure is real:

1. Re-run the failing test or check **alone**. If it passes in isolation it is a **flake**: record it and do not charge the attempt.
2. Only a failure that reproduces identically twice counts as real.
3. Charge a real failure against the stage's budget, then retry until the budget is spent.

The budget is **3 real attempts per stage**, tracked in agent state so it survives a stage handoff or a container restart:

```bash
human state incr <SEC_KEY> budget.<stage>.flakes     # a failure that vanished in isolation
human state incr <SEC_KEY> budget.<stage>.attempts   # a failure that reproduced
human state get  <SEC_KEY> budget.<stage>.attempts --default 0
```

An infrastructure failure — a dead container, a network blip, a runner that never started — is never a real attempt. It is a `retryable` exit (see the exit contract below); say so instead of burning the budget on it.

Exhausting the budget is an honest `needs-human-work` ending, not a silent stop: post the terminal marker the step calls for and report what the three attempts each tried and why each failed.

<!-- human:include exit-contract -->

<!-- human:include model-tiers -->

The tiers this pipeline uses, unless you have a reason to differ: triage, planner, reviewer and every adversarial check at `opus`; bug-fixer and security-verify at `sonnet`; preflight inherits.

## Step 1 — Parse argument

`$ARGUMENTS` is the security ticket key — the PM ticket — optionally followed by `--board`. Take the first non-flag token as `<SEC_KEY>`. Resolve the ticket with `human get <SEC_KEY>` — the CLI auto-detects the owning tracker from the key shape; `human tracker list` only enumerates trackers and must not be used to guess a key's owner. Call the tracker `<tracker>`.

### Step 1a — Preflight (ask once, up front, or not at all)

Before any work, run preflight. It resolves what this run may do, settles what the evidence can settle, and surfaces a decision only a human can make **now** rather than halfway through:

```
Task(subagent_type="human-preflight", prompt="Preflight security ticket <SEC_KEY> before an autonomous fix run: resolve capabilities, mirror decisions already made, and surface any genuine product/scope fork as a DECISION REQUIRED terminal.")
```

Read its outcome from state:

```bash
human state get <SEC_KEY> stage.preflight --field ready       # yes | no
human state get <SEC_KEY> stage.preflight --field question    # the fork, when ready is "no"
```

- **ready: yes** — the capability set and any prior decisions are recorded; continue to Step 2.
- **ready: no** — preflight returned a `DECISION REQUIRED:` terminal. Surface it as the **existing** up-front decision block and STOP; do not triage, plan, or fix into an unmade decision:

  ```bash
  human marker post <SEC_KEY> options \
    --field stage=implementation \
    --field context="<the DECISION REQUIRED one-liner>" \
    --field 1="<first option>" \
    --field 2="<second option>"
  ```

  The board renders this as "Decision needed" and the card waits without being mistaken for a crash. When the human picks, the daemon records `[human:option-chosen]` and relaunches this run with the choice in hand; preflight then mirrors it into `decisions` and returns `ready: yes`. Do **not** invent a `needs-input` marker — this loop already exists.

The capability set is the single source of truth for the rest of the run — do not infer permissions from flags or env vars:

```json
{"board_context": true, "can_push": false, "can_open_pr": false, "owns_deploy": false, "workspace": "bind-mounted"}
```

**The rule is one line: attempt nothing the capability set forbids, and treat a missing capability as a boundary, never as a failure.**

Set `<BOARD_CONTEXT>` to the set's `board_context`. (`--board` in `$ARGUMENTS` is the daemon's explicit signal and still forces it true.) In board context the container holds no push/PR credentials and the daemon's Deploy stage owns push → PR → CI → merge on the host: the run stops before deploy, having run the review inline.

## Step 2 — Phase 1: Triage & threat-model (verdict)

Delegate to the **human-security-triage** agent:

```
Task(subagent_type="human-security-triage", model="opus", prompt="Triage security ticket <SEC_KEY>: confirm whether the reported weakness is a real, exploitable vulnerability by tracing the source→sink data flow with file:line evidence, build the threat model (attacker, vector, asset, impact), rate severity, scan for sibling occurrences of the same weakness, and reach a verdict. Post the verdict comment on the ticket with a plain-language Explanation a non-engineer can follow. Keep exploit specifics on the tracker.")
```

It posts a `[human:bug-verdict] <verdict>` comment on the ticket — the permanent record: a plain-language explanation, the threat model, the severity, the source→sink cause chain, the regression window, and sibling occurrences. **Read the verdict from state, not from the agent's prose:**

```bash
human state get <SEC_KEY> stage.triage --field verdict     # confirmed | not-a-bug | undetermined
human state get <SEC_KEY> stage.triage --field severity    # critical | high | medium | low
human state get <SEC_KEY> stage.triage --field root_cause
```

The agent records `stage.triage` before returning (per the exit contract). Its message is for a human reader; the state record is what you branch on. If `stage.triage` is missing, the stage did not complete: treat that as `retryable` and re-dispatch rather than guessing a verdict from the text — **at most twice**. A record still missing after two re-dispatches is a broken state store (most often a daemon that predates `human state`); stop then with `needs-human-work`, naming state as the suspect:

```bash
human state incr <SEC_KEY> budget.triage.missing              # count this miss
human state get  <SEC_KEY> budget.triage.missing --default 0  # at 3, stop
```

If the recorded analysis stops at a proximate cause (a reachable sink without *why* the guard is missing or bypassable), re-dispatch the triage agent once, telling it which link is unproven — do not carry a shallow root cause into the plan.

## Step 3 — Verdict gate

- **confirmed** — continue to Step 4.
- **not-a-bug** or **undetermined** — do NOT act on the verdict yet. A no-fix verdict on a security report is the one outcome that can silently leave a live vulnerability open, so it must first survive an adversarial challenge (Step 3a).

### Step 3a — Adversarial challenge (not-a-bug / undetermined only)

Dispatch the skeptic against the verdict, with a security lens:

```
Task(subagent_type="human-verdict-skeptic", model="opus", prompt="Challenge the latest bug-verdict on security ticket <SEC_KEY>. Lens: try to reach the sink. Attempt to prove the path IS exploitable — an unsanitized source, an auth check that can be bypassed, a guard that does not cover the payload — before the vulnerability is dismissed.")
```

Read its outcome from state:

```bash
human state get <SEC_KEY> stage.challenge --field challenge   # upheld | refuted
```

- **UPHELD** — the verdict stands; act on it:
  - **not-a-bug** — close the ticket with `human close <SEC_KEY>` (closed-type status, falling back to done-type). Make **no code changes**. Post `human marker post <SEC_KEY> no-fix-needed --field verdict=not-a-bug --field challenge=upheld`, then Report and STOP.
  - **undetermined** — make **no code changes**. Leave the ticket open for a human. Post `human marker post <SEC_KEY> no-fix-needed --field verdict=undetermined --field challenge=upheld`, then Report and STOP.
- **REFUTED** — the vulnerability is real after all. Post the skeptic's evidence as a confirmed verdict:

  ```bash
  human marker post <SEC_KEY> bug-verdict --head confirmed --body-file - <<'EOF'
  ## Verdict overturned on adversarial challenge
  <the skeptic's refutation: the reachable path, the bypassable guard, or the crafted payload that succeeds>
  EOF
  ```

  Then **continue to Step 4 as a confirmed vulnerability**, using the skeptic's exploit as the reproduction. The challenge runs ONCE — a refuted verdict never loops back through triage.

The `[human:no-fix-needed]` marker is **mandatory in board context**: the pipeline runs under the board implementation-stage agent name, whose failure watcher treats any exit with no `[human:ready-for-review]` handoff as a crash and would loop forever re-triaging. This terminal marker signals the clean, resolved stop.

## Step 4 — Phase 2: Plan (topology decides where it lives)

1. Resolve the topology with `human tracker topology` — `{"topology":"single"|"split","pm":{...},"engineering":{...}}`.
   - **Split topology** — note `<ENG_TRACKER>` and `<ENG_PROJECT>`. The plan becomes a separate engineering ticket.
   - **Single-tracker topology** — the plan becomes a `[human:plan]` comment on the security ticket itself; no second ticket.
2. Delegate to the **human-planner** agent, seeding it with the triage threat model and root cause:

```
Task(subagent_type="human-planner", model="opus", prompt="Create an implementation plan to fix security ticket <SEC_KEY>. Decisions already settled (do not re-open): <paste `human state get <SEC_KEY> decisions --default '{}'`>. The threat model and source→sink root cause from triage:\n<paste the triage threat model + root cause + fix outline>\nThe plan's Changes section MUST begin with adding a SECURITY regression test that demonstrates the exploit and fails because of the vulnerability, then closing the root cause at the source (parameterize/encode/guard/tighten the check — not merely suppress the symptom). Address the sibling occurrences from triage or state them out of scope. Return the plan as output; do not write files or create tickets.")
```

Capture the output as `<PLAN_CONTENT>`. Ensure its header has a `**PM ticket**: <SEC_KEY>` line and, in split topology, an `**Engineering ticket**: TBD` line.

3. Attach the plan.

**Split topology** — create the engineering ticket:

```bash
human <ENG_TRACKER> issue create --project=<ENG_PROJECT> "Fix: <short vulnerability summary>" --description "$(cat <<'PLAN_EOF'
<PLAN_CONTENT>
PLAN_EOF
)"
```

Capture `<ENG_KEY>`, update its description so the `**Engineering ticket**:` line reads `<ENG_KEY>`. Set `<WORK_KEY>` to `<ENG_KEY>`.

**Single-tracker topology** — post the plan as a `[human:plan]` comment:

```bash
human marker post <SEC_KEY> plan --body-file - <<'PLAN_EOF'
<PLAN_CONTENT>
PLAN_EOF
```

Verify with `human plan show <SEC_KEY>`. Commits reference only `<SEC_KEY>`. Set `<WORK_KEY>` to `<SEC_KEY>`.

## Step 5 — Phase 3: Test-first fix

Delegate to the **human-bug-fixer** agent (shared with autofix — it writes the failing regression test and the root-cause fix; the security framing comes from the plan it reads). When `<BOARD_CONTEXT>` is true the fixer must NOT push — forward the board instruction explicitly (the fixer cannot see `$ARGUMENTS`):

```
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="Fix ticket <WORK_KEY> (PM security ticket <SEC_KEY>) test-first on a feature branch, following the plan. The plan's first change is a SECURITY regression test that demonstrates the exploit and must fail before the fix. BOARD CONTEXT: do NOT run git push — leave the branch local; the daemon's Deploy stage ships it. Report the local branch name. Iterate on the fast test+lint tier (not the full `make check`) to go green — the verify gate runs the single full suite. Keep exploit specifics out of commit messages — reference the ticket and describe the hardening.")
```

Otherwise (standalone, `<BOARD_CONTEXT>` false) dispatch the push variant:

```
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="Fix ticket <WORK_KEY> (PM security ticket <SEC_KEY>) test-first on a feature branch and push it, following the plan (its first change is a security regression test that demonstrates the exploit and fails before the fix). Iterate on the fast test+lint tier to go green — the verify gate runs the single full suite. Keep exploit specifics out of commit messages.")
```

It creates branch `autofix/<work-key>` (the key lowercased), writes the security regression test, implements the root-cause fix, confirms the suite is green, commits with subjects starting with the `human commits prefix <SEC_KEY> [<ENG_KEY>]` prefix, and returns the branch name. In a standalone run it pushes; in board context it leaves the branch local. If it reports it could not go green, STOP and report — do not open a PR.

## Step 6 — Phase 4: Verify (done gate — the attack no longer works)

Delegate to the **human-security-verify** agent:

```
Task(subagent_type="human-security-verify", model="sonnet", prompt="Verify ticket <WORK_KEY> (PM security ticket <SEC_KEY>): confirm the security regression test demonstrates the exploit and fails before / passes after the fix, the vulnerability from the triage threat model is actually closed (the attack no longer succeeds), the full suite is green, and no new weakness was introduced. Post the verdict as a comment on <SEC_KEY>.")
```

This is the pipeline's ONE full-suite pass; the fixer used the fast tier. Ensure the `[human:bug-verify]` comment records the `## Evidence` block and the `## Vulnerability closed` finding so the review can trust it without re-running the suite.

**Read the gate's outcome from state:**

```bash
human state get <WORK_KEY> stage.verify --field verdict   # DONE | NOT DONE
human state get <WORK_KEY> stage.verify --field gaps      # what is still missing, when NOT DONE
```

If the verdict is NOT DONE, re-run Step 5 to address the gaps, under the retry budget above. Once the budget is spent, do NOT stop silently — a silent stop freezes the card at "being fixed" forever. Post an explicit terminal marker so the board reds the card to a needs-attention/Retry badge:

```bash
human marker post <SEC_KEY> implementation-failed --body-file - <<'EOF'
<one-line verdict headline — becomes the card's badge text>

<the security-verify gaps: what is still NOT DONE and why the vulnerability is not yet closed>
EOF
```

The first body line becomes the badge headline. This is mandatory in board context. Then STOP and report honestly without posting the handoff.

## Step 7 — Phase 5: Hand off and security review

Only after a DONE verdict.

### 7.1 Post the review handoff

Post the review handoff on the security (PM) ticket — the **same handoff the kanban executor posts**, so the trail and the board's `(R)` annotation work identically:

```bash
human handoff post <SEC_KEY> --engineering <ENG_KEY> --branch autofix/<work-key>   # split topology
human handoff post <SEC_KEY> --branch autofix/<work-key>                           # single-tracker: omit --engineering
```

The command derives `commits:` and `daemon:`, verifies every SHA is reachable on the branch (fetching origin first), and refuses to post otherwise. If the handoff cannot be posted (non-zero exit), STOP with an honest status report — **do not report success**.

**Board-context exception applies here**: when `<BOARD_CONTEXT>` is true, post the handoff (so `branch:`/`commits:` are recorded for the Deploy button), then CONTINUE to the inline review (Steps 7.2–7.3) in this same warm container. STOP after the review (do not run Step 8 / deploy). Do NOT push or `git ls-remote` — the branch is intentionally local.

### 7.2 Security review by the reviewer agent

Chain straight into the review. This runs **inline in this same warm container in board context too** (SC-782). Post the started marker, then dispatch the reviewer **with a security lens**:

```bash
human marker post <SEC_KEY> review-started
```

```
Task(subagent_type="human-reviewer", model="opus", prompt="Review changes for security ticket <WORK_KEY>: check out branch autofix/<work-key> and review its diff against main against the ticket's plan and acceptance criteria. Security lens: confirm the fix closes the vulnerability at its source (not just the reported payload), covers the sibling occurrences the triage found, and introduces no new weakness (an over-broad allow, a widened permission, a secret written to a log, a check that can still be bypassed). The security regression test must genuinely exercise the exploit.")
```

The reviewer writes `.human/reviews/<work-key>.md` and records its outcome in state. **Read the verdict from state, never from the file's prose:**

```bash
human state get <WORK_KEY> stage.review --field verdict   # pass | pass with notes | fail | unreviewable
human state get <WORK_KEY> stage.review --field reason    # why, when unreviewable
```

Post the outcome on the security ticket with the reviewer's **full findings** inlined under a `## Findings` section (the board detail panel shows it without opening the local `.human/reviews/<work-key>.md`):

```bash
human marker post <SEC_KEY> review-complete \
  --field verdict="<verdict>" \
  --field reviews="<WORK_KEY>: <verdict> — .human/reviews/<work-key>.md" \
  --body-file - <<'REVIEW_EOF'
## Findings
<the reviewer's full findings, inlined: what was checked, every issue found
 (or "no issues"), and whether the vulnerability is closed at the source>
REVIEW_EOF
```

### 7.3 Review gate

- **pass** or **pass with notes** — a pass is about to be made irreversible by a merge. Before continuing, get one adversarial second opinion **through a security lens**:

  ```
  Task(subagent_type="human-second-opinion", model="opus", prompt="The pipeline is about to merge branch autofix/<work-key> for security ticket <WORK_KEY> on the strength of a passing review. Lens: is-the-vulnerability-actually-closed. Evidence: the ticket's threat model, the branch diff against main, and stage.review in agent state. Try to refute that the fix closes the source→sink path — look for a residual bypass, an uncovered sibling, or a new weakness the fix introduced. Do not read the reviewer's reasoning first.")
  ```

  ```bash
  human state get <WORK_KEY> stage.opinion --field opinion    # upheld | refuted
  ```

  - **upheld** — continue to Step 8.
  - **refuted** — treat it exactly like a failing review: feed its evidence back to the fixer under the review budget (the `fail` branch below). Do not merge on a refuted pass.

  Run this once per review verdict, not once per attempt.
- **unreviewable** — the reviewer could not obtain the code, so there are NO findings. Do NOT re-dispatch the fixer and do NOT post `[human:review-complete] verdict: fail`. Instead post `[human:review-failed]` naming the unreachable ref — `human marker post <SEC_KEY> review-failed --field reason="<reachability reason>"` — then STOP (report per Step 9). No PR is merged.
- **fail** — feed the reviewer's findings back: re-dispatch the **human-bug-fixer** (Step 5) with the findings appended, re-run the verify gate (Step 6), then re-run the review (7.2, one new `[human:review-complete]` comment). This loops under the retry budget (`budget.review.attempts`). When the budget is spent, STOP honestly as `needs-human-work`: the handoff stays standing and NO pull request is merged.

## Step 8 — Phase 6: Deploy — end with a merged PR

Only after a passing review. This is the board's deploy pipeline (push → PR → CI gate → merge → close) driven from the skill:

1. Post the started marker: `human marker post <SEC_KEY> deploy-started`.
2. Run the deploy gate:

   ```bash
   human deploy <SEC_KEY> --branch autofix/<work-key> --title "[<SEC_KEY>] [<ENG_KEY>] <short summary>"
   ```

   (single-tracker: only `[<SEC_KEY>]` in the title). Keep the title and PR body free of exploit detail. The command owns the whole gate: push + PR, the CI wait, rebase-if-stale, merge, remote-branch cleanup, the `[human:deployed]` marker with its `pr:` line, and the ticket close. A `[human:deploy-failed]` is an honest needs-human end state, not a first-failure stop: do NOT merge by hand and do NOT re-implement the reviewed work; the PR stays open for a human with the named blocker.
3. In split topology, close `<ENG_KEY>` as well: `human done <ENG_KEY>`.
4. For the Step 9 report, read `<PR_URL>` from the deployed marker if needed: `human marker show <SEC_KEY> deployed`.

## Step 9 — Run summary: ticket comment, then report

Once a fix was attempted (Step 4 ran), the ticket must carry a plain-language account of the run. Post it at EVERY terminal point after Step 4: the board-context stop after the handoff (7.1), a shipped fix (Step 8), and every honest STOP. Runs that end at the verdict gate (Step 3) post nothing here — the triage verdict comment already tells that story. **Keep exploit specifics out of this summary if the ticket is publicly visible — describe the risk and the hardening, not a working attack.**

```bash
human marker post <SEC_KEY> fix-summary --body-file - <<'SUMMARY_EOF'
## What happened
<2–4 sentences, plain language: what the vulnerability turned out to be, what was at risk, and what the fix closes. Written for the reporter, not an engineer.>

## Changes
- Branch: autofix/<work-key> — <left local for Deploy | pushed | merged as <PR_URL>>
- Commits: <short sha — one-line subject, per commit>
- <the areas of the product hardened, one line>

## Proof
- Security regression test: <name/location> — demonstrated the exploit; failed before the fix, passes after
- Vulnerability closed: <the source→sink path is now broken — one line>
- Checks: <suite/lint/coverage result>
- Review: <verdict, or "pending — daemon chains it" in board context>

## Along the way
<the story of the run when it was not straight: a refuted verdict, a first verify that came back not-DONE because the exploit was still reachable, review findings addressed. If it went straight through, say exactly that.>

## Where it ended
<board: handoff posted, the Deploy button ships it | standalone: PR merged, ticket closed by the deploy gate | stopped at <step>: what a human needs to do next>
SUMMARY_EOF
```

Fill every section from what actually happened in THIS run — never leave template placeholders. If posting the summary fails, still produce the final report below.

Then report the verdict. For a confirmed, shipped fix, present the traceability chain:

```
Security fix complete for <SEC_KEY>

Verdict: confirmed (<severity>) — review: <verdict> — shipped
- PM ticket:  <tracker> <SEC_KEY>
- Threat:     [human:bug-verdict] comment on <SEC_KEY> (threat model + severity + cause chain)
- Plan:       <ENG_TRACKER> <ENG_KEY> (split topology) — or [human:plan] comment on <SEC_KEY>
- Branch:     autofix/<work-key>
- Review:     [human:review-complete] verdict: <verdict> on <SEC_KEY>
- PR:         <PR_URL> — merged, branch deleted
- Ticket:     closed by the deploy gate (`human deploy`)
```

For a board-context run (exception in Step 7.1) or a failed review/deploy gate, report where the pipeline stopped, which marker records it, and what a human needs to do next.
