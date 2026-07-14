---
name: human-sprint
description: Auto-pipeline from idea to implementation-ready tickets (or full implementation)
argument-hint: <rough idea or topic>
---

# Overview

This skill chains the full human pipeline into a single flow: **Ideate -> Plan -> Execute -> Review**. It auto-decides mechanical questions using encoded decision principles and only surfaces genuine taste decisions to the user. The user can stop at any phase.

## Decision Principles (for auto-deciding mechanical questions)

- **Narrowest wedge first**: Choose the smallest scope that delivers value
- **Completeness within scope**: Whatever is in scope should be complete
- **Explicit > clever**: Prefer obvious solutions over clever ones
- **Reuse > reinvent**: Use existing patterns and code before creating new ones
- **Bias toward action**: When in doubt, lean toward building

## Taste Decisions (always surface to user via AskUserQuestion)

- Scope expansion vs. reduction
- Architecture choices with genuine tradeoffs
- When product and engineering perspectives conflict
- Pipeline depth (how far to go)

---

Follow these steps in order:

## Step 1 — Parse arguments

Parse `$ARGUMENTS` as a rough idea or topic. Set `<slug>` to a slugified version (lowercase, spaces to hyphens, strip special chars, max 50 chars).

## Step 2 — Ask pipeline depth

Ask the user via `AskUserQuestion`:

"How far should the pipeline go?
- (A) **Tickets only** — create PM ticket + engineering plan, then stop
- (B) **Plan + execute** — create tickets and implement the plan
- (C) **Full pipeline** — create tickets, implement, and run a review"

Store the user's choice as `<pipeline_depth>`.

## Step 3 — Phase 1: Ideate (creates PM ticket)

1. Create the output directory: `mkdir -p .human/ideation`

2. Delegate to the **human-ideator** agent (Phase 1 — context gathering):

```
Task(subagent_type="human-ideator", prompt="Phase 1: Gather context for the idea: $ARGUMENTS. Explore the codebase for relevant code, existing patterns, recent git history, existing tickets, and any .human/ artifacts. Return a context summary and suggested forcing questions.")
```

3. Present the agent's context summary to the user.

4. Ask forcing questions one at a time using `AskUserQuestion`. Ask each question individually, collecting the answer before proceeding to the next:
   - "What is the actual pain? (not the feature request — what hurts today?)"
   - "Who has this pain? (specific users or personas, not hypothetical)"
   - "What is the status quo? (how do people cope without this?)"
   - "What is the narrowest wedge? (the smallest version that delivers value)"
   - "What would make this a 10-star version? (dream big, then we scope back)"

5. Ask scope choice via `AskUserQuestion`: "Based on your answers, should we: (A) Expand — go broader than the narrowest wedge, (B) Hold — keep the narrowest wedge as described, or (C) Reduce — cut even further?"

6. Ask tracker choice via `AskUserQuestion`: "Which tracker should the PM ticket be created on? (e.g., 'shortcut' or 'linear')"

7. Delegate to the **human-ideator** agent (Phase 2 — generate ticket content):

```
Task(subagent_type="human-ideator", prompt="Phase 2: Generate PM ticket content for the idea: $ARGUMENTS. Forcing question answers: <paste all Q&A pairs>. Scope choice: <user's scope choice>. Tracker: <chosen tracker>. Generate a structured ticket with problem statement, user story, acceptance criteria, scope decisions, and challenge record.")
```

8. Create PM ticket on the chosen tracker:

```bash
human <tracker> issue create --project=<PROJECT> "Short title derived from the idea" --description "<structured ticket content from agent>"
```

9. Write ideation record to `.human/ideation/<slug>.md` including the full challenge record.

10. Store the PM ticket key as `<PM_TICKET_KEY>` and the tracker as `<PM_TRACKER>` for traceability.

## Step 3.5 — Resolve topology

Run `human tracker list` and check where the plan will live:

- **Split topology** — a tracker with `"role": "engineering"` exists and is a DIFFERENT tracker than the PM ticket's: the plan becomes a separate engineering ticket there. Ask the user via `AskUserQuestion` only if the tracker or project is ambiguous: "Which tracker should the engineering ticket be created on? (e.g. linear, jira, github, gitlab, azuredevops, shortcut)" and "What project should the ticket be created in? (e.g. 'HUM' for Linear, 'myorg/myrepo' for GitHub)". Store the answers as `<ENG_TRACKER>` and `<ENG_PROJECT>`.
- **Single-tracker topology** — no engineering-role tracker, or it is the same tracker as the PM ticket: no second ticket is created. The plan will be attached to the PM ticket itself as a `[human:plan]` comment; skip the questions.

## Step 4 — Phase 2: Plan (attaches the plan where topology says)

### Step 4a: Draft

Delegate to the **human-planner** agent to create the plan. The planner returns the plan as output (no files written):

```
Task(subagent_type="human-planner", prompt="Create an implementation plan for the idea described in .human/ideation/<slug>.md. The PM ticket is <PM_TICKET_KEY> on <PM_TRACKER>. Return the complete plan as your output. Do not write any files or create any tickets.")
```

Capture the output as `<PLAN_CONTENT>`.

### Step 4b: Verify (parallel)

Launch both verification agents in a single message, passing the plan inline:

```
Task(subagent_type="plan-verify-code", prompt="Verify all code references in the following implementation plan against the actual codebase. Return your verification report as output. Do not write any files.\n\n---BEGIN PLAN---\n<PLAN_CONTENT>\n---END PLAN---")

Task(subagent_type="plan-verify-docs", prompt="Verify all library, framework, and API assumptions in the following implementation plan against actual documentation and source. Return your verification report as output. Do not write any files.\n\n---BEGIN PLAN---\n<PLAN_CONTENT>\n---END PLAN---")
```

### Step 4c: Finalize and attach the plan

If verification found issues, fix `<PLAN_CONTENT>` accordingly. Then attach the plan according to the topology from Step 3.5:

**Split topology** — confirm the plan header contains a `**PM ticket**: <PM_TICKET_KEY>` line so the executor can reference both tickets in commits — add it if missing. Create the engineering ticket with the plan as the description:

```bash
human <ENG_TRACKER> issue create --project=<ENG_PROJECT> "Short title from plan" --description "$(cat <<'PLAN_EOF'
<FINAL_PLAN_CONTENT>
PLAN_EOF
)"
```

Store the engineering ticket key as `<ENG_TICKET_KEY>`. Then update the ticket description so the `**Engineering ticket**:` line in the plan header contains `<ENG_TICKET_KEY>` (replacing `TBD`). This gives the executor both keys from a single source. Set `<WORK_KEY>` to `<ENG_TICKET_KEY>`.

**Single-tracker topology** — post the plan verbatim as a `[human:plan]` marker comment on the PM ticket (no second ticket; the description stays product language):

```bash
human <PM_TRACKER> issue comment add <PM_TICKET_KEY> "$(cat <<'PLAN_EOF'
[human:plan]

<FINAL_PLAN_CONTENT>
PLAN_EOF
)"
```

Verify with `human plan show <PM_TICKET_KEY>` — it must print the plan back. The plan header needs no `**Engineering ticket**:` line, and commits reference only the PM key. Set `<WORK_KEY>` to `<PM_TICKET_KEY>`.

**If `<pipeline_depth>` is "Tickets only":** Stop here. Tell the user:
- PM ticket created: `<PM_TRACKER> #<PM_TICKET_KEY>`
- Split topology: engineering ticket created: `<ENG_TRACKER> <ENG_TICKET_KEY>`; single-tracker: plan attached as a `[human:plan]` comment on `<PM_TICKET_KEY>`
- Ideation record at `.human/ideation/<slug>.md`

## Step 5 — Mechanical decision gate

Before executing, load the plan — `human get <WORK_KEY>` for a plan in an engineering ticket description, `human plan show <WORK_KEY>` for a plan comment — and check for architecture choices or trade-offs in the plan:

- **If the plan has no taste decisions** (only mechanical implementation steps): Proceed automatically to execution. Apply the decision principles above.
- **If the plan contains trade-offs or architecture choices**: Present them to the user via `AskUserQuestion` and let the user decide before proceeding. Example: "The plan includes the following architecture choices that need your input: <list choices>. What is your preference for each?"

## Step 6 — Phase 3: Execute (implements the plan)

Delegate to the **human-executor** agent:

```
Task(subagent_type="human-executor", prompt="Execute <WORK_KEY> as a plan")
```

The executor fetches the ticket, reads the plan from the description or the `[human:plan]` comment, and implements it.

**If `<pipeline_depth>` is "Plan + execute":** Stop here. Tell the user:
- PM ticket: `<PM_TRACKER> #<PM_TICKET_KEY>`
- Split topology: engineering ticket `<ENG_TRACKER> <ENG_TICKET_KEY>`
- Implementation complete
- Suggest running `/human-review <WORK_KEY>` manually if desired

## Step 7 — Phase 4: Review (final quality gate)

Delegate to the **human-reviewer** agent:

```
Task(subagent_type="human-reviewer", prompt="Review changes for ticket <WORK_KEY>")
```

Present the review results to the user.

If the review finds issues, ask the user via `AskUserQuestion`: "The review found the following issues: <list issues>. Should we: (A) Fix the issues and re-review, or (B) Accept as-is?"

If the user chooses to fix:
- Address each issue found by the reviewer
- Re-run the review: `Task(subagent_type="human-reviewer", prompt="Review changes for ticket <WORK_KEY>")`

## Step 8 — Summary

Present the full traceability chain to the user:

Split topology:

```
Sprint complete for: <original idea>

Traceability:
- PM ticket:    <PM_TRACKER> #<PM_TICKET_KEY> — the original idea
- Eng ticket:   <ENG_TRACKER> <ENG_TICKET_KEY> — the implementation plan
- Commits:      reference both tickets

Artifacts:
- Ideation:     .human/ideation/<slug>.md
- Review:       .human/reviews/<key>.md
```

Single-tracker topology:

```
Sprint complete for: <original idea>

Traceability:
- Ticket:       <PM_TRACKER> #<PM_TICKET_KEY> — the idea, its plan ([human:plan] comment), and the review trail
- Commits:      reference the single ticket key

Artifacts:
- Ideation:     .human/ideation/<slug>.md
- Review:       .human/reviews/<key>.md
```
