---
name: human-plan
description: Fetch an issue tracker ticket and create an implementation plan
argument-hint: <ticket-key>
---

# Implementation Plan Pipeline

Create an implementation plan using a 3-phase agent pipeline: draft, verify, finalize. The plan is embedded directly in the engineering ticket description — no plan files are created.

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
- Proceed directly to creating the engineering ticket.

If the verification reports found **issues** (mismatches, missing references, unaccounted callers, deprecations, or unverifiable claims):
- Update `<PLAN_CONTENT>` to fix all verified issues:
  - Correct wrong signatures, types, or file paths
  - Add handling for unaccounted callers/dependents
  - Replace deprecated APIs with their replacements
  - Mark unverifiable claims with "UNVERIFIED — confirm before implementing"

## Phase 4: Confidence check

After finalizing the plan, review it yourself end-to-end:

1. For every API call, library function, or external integration in the plan, verify the function signatures, parameters, and return types against real documentation (use `WebFetch` or `WebSearch` if needed) or against the actual source code in the codebase.
2. For every file path, function name, type, and interface referenced in the plan, grep the codebase to confirm they exist and match the plan's assumptions.
3. Rate your confidence that the plan can be implemented as-is without the executor needing to make design decisions or discover missing information. If you are not fully confident:
   - Fix every gap, wrong assumption, or ambiguity in the plan now.
   - Re-verify the fixes against docs and code.
   - Repeat until you are confident the plan is correct and complete.

Only proceed to ticket creation once you are confident the plan will work.

## Phase 5: Create ticket

Then resolve the engineering tracker: run `human tracker list` and pick the tracker with `"role": "engineering"`. Use its `type` as the tracker and its first configured project. If no tracker has role `engineering`, fall back to asking the user via `AskUserQuestion`.

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

## Phase 6: Post the plan-ready marker on the PM ticket

After the engineering ticket exists, post a structured marker comment on the **PM ticket** so the workflow board can advance the card from Planning into Implementation (the board resolves the Implementation input from this `engineering:` line). The format is fixed so it can be parsed unambiguously across trackers:

```
[human:plan-ready]
engineering: <ENG_KEY>
```

Post it with `human <pm-tracker> issue comment add <PM_KEY> "<comment-body>"`, where `<pm-tracker>` is the PM tracker resolved from `human tracker list` (the one with `"role": "pm"`), `<PM_KEY>` is the original PM ticket key from the plan's `**PM ticket**:` header, and `<ENG_KEY>` is the engineering ticket key just created. This mirrors the `[human:ready-for-review]` handoff that `human-executor` posts after implementation.

## After completion

Tell the user:
- A short summary of the plan (3-5 bullet points: what will change, key files, risks)
- Whether verification found issues and what was corrected
- The engineering ticket key
