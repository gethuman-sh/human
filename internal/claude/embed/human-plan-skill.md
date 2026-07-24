---
name: human-plan
description: Fetch an issue tracker ticket and create an implementation plan
argument-hint: <ticket-key>
---

# Implementation Plan Pipeline

Create an implementation plan using a 3-phase agent pipeline: draft, verify, finalize. No plan files are created — the plan lands on the tracker: as a separate engineering ticket's description (split topology: distinct PM and engineering trackers) or as a `[human:plan]` comment on the ticket itself (single-tracker topology).

## Phase 0: Inherit the chosen design

Run `human mockups chosen <KEY>` (the PM key this skill received). If it prints a path, read that HTML file — it is the human-selected design direction (the winning mockup) for this ticket. Treat it as authoritative UI/interaction context: the plan MUST build the UI to match it, and the planner prompt should carry that design as context. If it prints nothing, there is no chosen design; proceed normally.

## Phase 1: Draft Plan

Run the planner agent. It returns the plan as its output (no files written):

```
Task(subagent_type="human-planner", prompt="Create an implementation plan for ticket $ARGUMENTS. Return the complete plan as your output. Do not write any files.")
```

Wait for the planner agent to finish. Capture its output as `<PLAN_CONTENT>`.

## Phase 2: Verify (parallel)

Launch both verification agents **in a single message** so they run in parallel. Pass the plan content inline using markers. Each agent returns its report as output (no files written):

```
Task(subagent_type="plan-verify-code", prompt="Verify all code references in the following implementation plan against the actual codebase. Return your verification report as output. Do not write any files.\n\n---BEGIN PLAN---\n<PLAN_CONTENT>\n---END PLAN---")

Task(subagent_type="plan-verify-docs", prompt="Verify all library, framework, and API assumptions in the following implementation plan against actual documentation and source. Return your verification report as output. Do not write any files.\n\n---BEGIN PLAN---\n<PLAN_CONTENT>\n---END PLAN---")
```

Wait for both agents to finish before proceeding.

## Phase 3: Finalize

Read both verification reports from the agent outputs.

If the verification reports found **no issues** (all OK, zero mismatches, zero missing):
- Proceed directly to attaching the plan (Phase 5).

If the verification reports found **issues** (mismatches, missing references, unaccounted callers, deprecations, or unverifiable claims):
- Update `<PLAN_CONTENT>` to fix all verified issues:
  - Correct wrong signatures, types, or file paths
  - Add handling for unaccounted callers/dependents
  - Replace deprecated APIs with their replacements
  - Mark unverifiable claims with "UNVERIFIED — confirm before implementing"

## Phase 3a: Already-implemented terminal (nothing to plan)

If the planner returned an `ALREADY IMPLEMENTED: <evidence>` verdict instead of a plan — exploration showed every acceptance criterion is already satisfied by code merged on `main` — the ticket's work has already shipped. Attaching a plan and posting `[human:plan-ready]` would advance the card and re-implement shipped code, so do NOT do that. Instead:

- Do NOT run the verification phases, attach any plan, or post `[human:plan-ready]`.
- Post the terminal `[human:nothing-to-do]` marker on the PM ticket, carrying the planner's evidence (name the merged PR/commit) so the board surfaces the card as "already shipped" (resolved), not red:

```bash
human marker post <PM_KEY> nothing-to-do --field "evidence=<the planner's ALREADY IMPLEMENTED evidence — merged PR/commit>"
```

- STOP. Skip Phases 4-6 entirely. In board context this is mandatory: the workflow board's failure watcher treats `[human:nothing-to-do]` as a clean stop (resolved, no retry loop), whereas a missing `[human:plan-ready]` after a normal exit is misread as a crash and re-planned forever.

## Phase 3b: Decision-required terminal (up-front human fork)

If the planner returned a `DECISION REQUIRED:` verdict instead of a plan — the plan hinges on a product/UX or ambiguity fork only a human can settle — do NOT invent a plan and do NOT proceed to implementation with the decision baked in as a mid-run gate (that is the stranded-run failure this guards against). Surface the fork as an up-front `[human:options]` decision block on the PM ticket and STOP:

- Do NOT run the verification phases, attach any plan, or post `[human:plan-ready]`.
- Post the options block with stage `planning`, so the human's pick re-runs planning with the decision recorded. Map the verdict's first line to `context`, and each `N:` line to a `--field N=`:

```bash
human marker post <PM_KEY> options \
  --field stage=planning \
  --field context="<the DECISION REQUIRED one-liner>" \
  --field 1="<first option>" \
  --field 2="<second option>"
```

- STOP. Skip Phases 4-6 entirely. The board renders the options and waits; when the human picks, the daemon relaunches `/human-plan <PM_KEY>` with the choice injected as a `[human:option-chosen]` comment, and the planner then produces a fully autonomous, gate-free plan for that direction. In board context this is mandatory: posting the options block (not a plan-ready) is what lets planning pause cleanly instead of dispatching implementation into a decision no one is present to make.

## Phase 4: Confidence check

After finalizing the plan, review it yourself end-to-end:

1. For every API call, library function, or external integration in the plan, verify the function signatures, parameters, and return types against real documentation (use `WebFetch` or `WebSearch` if needed) or against the actual source code in the codebase.
2. For every file path, function name, type, and interface referenced in the plan, grep the codebase to confirm they exist and match the plan's assumptions.
3. Rate your confidence that the plan can be implemented as-is without the executor needing to make design decisions or discover missing information. If you are not fully confident:
   - Fix every gap, wrong assumption, or ambiguity in the plan now.
   - Re-verify the fixes against docs and code.
   - Repeat until you are confident the plan is correct and complete.
4. Scan the finalized plan for any mid-execution human gate — a step that waits for sign-off, approval, confirmation, or a user decision. There must be none. If the plan can only proceed past such a step with human input, that decision belonged up front: discard the plan and re-run planning so the planner emits the `DECISION REQUIRED:` terminal instead (Phase 3b).

Only proceed to ticket creation once you are confident the plan will work.

## Phase 5: Attach the plan (topology decides where)

Run `human tracker topology`:

- **Split topology** — the output says `"topology": "split"` and its `engineering` entry names the tracker for the engineering ticket: create a separate engineering ticket there (steps below).
- **Single-tracker topology** — the output says `"topology": "single"` (no `engineering` entry): do NOT create a second ticket. The plan lives on the PM ticket itself as a `[human:plan]` comment (Phase 5b).

### Phase 5a: Split topology — create the engineering ticket

Confirm the plan has a `**PM ticket**:` line in its header referencing the original PM ticket key. If it is missing, add it before creating the engineering ticket so the executor can reference both tickets in commits.

Create the engineering ticket with the plan as the description. The ticket description **must be a 1:1 verbatim copy** of `<FINAL_PLAN_CONTENT>` — do not summarize, reformat, truncate, reorder, or rewrite any part of the plan. Every section, bullet, code block, and line must appear in the ticket exactly as in the final plan. Use a heredoc to preserve special characters and formatting:

```bash
human <tracker> issue create --project=<PROJECT> "Short title from plan" --description "$(cat <<'PLAN_EOF'
<FINAL_PLAN_CONTENT>
PLAN_EOF
)"
```

After creating the ticket, capture the returned engineering ticket key and update the ticket description so the `**Engineering ticket**:` line in the plan header contains the actual key (replacing `TBD`). This gives the executor both the PM and engineering ticket keys from the plan header so every commit can reference both.

Then fetch the ticket back and verify the description matches the updated plan content byte-for-byte. If it does not match, update the ticket until it does.

### Phase 5b: Single-tracker topology — attach the plan as a comment

Post the plan verbatim as a `[human:plan]` marker comment on the PM ticket (the ticket description stays product language; the plan is a stage artifact and lives in the comment stream):

```bash
human marker post <PM_KEY> plan --body-file - <<'PLAN_EOF'
<FINAL_PLAN_CONTENT>
PLAN_EOF
```

Verify with `human plan show <PM_KEY>` — it must print the plan back. Re-planning posts a new `[human:plan]` comment; the latest wins, never edit old ones. In this topology the plan header needs no `**Engineering ticket**:` line, and commits reference only the PM key.

## Phase 6: Post the plan-ready marker on the PM ticket

Post a structured marker comment on the **PM ticket** so the workflow board can advance the card from Planning into Implementation. The format is fixed so it can be parsed unambiguously across trackers:

- Split topology (engineering ticket created):

```bash
human marker post <PM_KEY> plan-ready --field engineering=<ENG_KEY>
```

which renders as:

```
[human:plan-ready]
engineering: <ENG_KEY>
```

- Single-tracker topology (plan attached as comment) — no `engineering:` field; the board dispatches Implementation on the PM key itself:

```bash
human marker post <PM_KEY> plan-ready
```

`<PM_KEY>` is the original PM ticket key from the plan's `**PM ticket**:` header. This mirrors the `[human:ready-for-review]` handoff that `human-executor` posts after implementation.

## Retry budgets, flakes, and how this run may end

The board's planning stage runs this skill, so it must recover like every other stage rather than stopping at the first failure.

A verification pass or a tool call that fails is not automatically a failed plan. Re-run the failing step **alone** first: if it succeeds in isolation it is a flake — record it and retry without charging an attempt. Only a failure that reproduces identically twice is real. The budget is **3 real attempts**:

```bash
human state incr <PM_KEY> budget.planning.flakes      # vanished in isolation
human state incr <PM_KEY> budget.planning.attempts    # reproduced
human state get  <PM_KEY> budget.planning.attempts --default 0
```

Infrastructure trouble — a dead container, a network blip — is never a real attempt; it is a `retryable` ending, and the board relaunches the stage automatically rather than reddening the card.

Record the outcome before finishing, so the board can tell a glitch from a blocker:

```bash
human state set <PM_KEY> stage.planning --json --body-file - <<'EOF'
{"exit":"done",
 "outcome":"<plan-ready|nothing-to-do|decision-required>",
 "summary":"<one line>",
 "evidence":"<the marker or ticket that carries the result>"}
EOF
```

<!-- human:include exit-contract -->

## After completion

Tell the user:
- A short summary of the plan (3-5 bullet points: what will change, key files, risks)
- Whether verification found issues and what was corrected
- Where the plan landed: the engineering ticket key (split topology) or the `[human:plan]` comment on the ticket (single-tracker topology)
