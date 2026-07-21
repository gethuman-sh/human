---
name: human-gardening
description: Analyze codebase health and suggest refactorings using a multi-agent pipeline
---

# Codebase Health Gardening

Analyze this codebase for structural debt, duplication, complexity hotspots, and hygiene issues using a 4-phase agent pipeline: survey, deep analysis, triage, and optional fix.

## Phase 1: Survey

Initialize the pipeline, then run the survey agent:

```bash
human pipeline init gardening
```

This creates `.human/gardening/` and prints the pipeline paths (`root`, `candidates`, `state`). Note the `candidates` path — you pass it to the triage agent in Phase 3.

```
Task(subagent_type="gardening-survey", prompt="Survey this codebase for health analysis. Write your survey report to .human/gardening/.gardening-survey.md")
```

Wait for the survey agent to finish before proceeding.

## Phase 2: Deep Analysis (parallel)

Launch all 4 analysis agents **in a single message** so they run in parallel:

```
Task(subagent_type="gardening-structure", prompt="Read the survey report at .human/gardening/.gardening-survey.md, then analyze the codebase for architectural imbalances, misplaced types, and leaky abstractions. Report each finding with `human pipeline append gardening`")

Task(subagent_type="gardening-duplication", prompt="Read the survey report at .human/gardening/.gardening-survey.md, then analyze the codebase for structural clones, repeated patterns, and extractable utilities. Report each finding with `human pipeline append gardening`")

Task(subagent_type="gardening-complexity", prompt="Read the survey report at .human/gardening/.gardening-survey.md, then analyze the codebase for long functions, deep nesting, cyclomatic complexity, and dead code. Report each finding with `human pipeline append gardening`")

Task(subagent_type="gardening-hygiene", prompt="Read the survey report at .human/gardening/.gardening-survey.md, then analyze the codebase for naming inconsistencies, test health issues, dependency problems, and convention violations. Report each finding with `human pipeline append gardening`")
```

The append command writes all findings into the shared candidates file race-free, so the agents can run in parallel without coordinating.

Wait for all 4 agents to finish before proceeding.

## Phase 3: Triage

Check how many candidates the analysis agents reported with `human pipeline count gardening`, then run the triage agent to validate, assess compound impact, and produce the final report with health scorecard:

```
Task(subagent_type="gardening-triage", prompt="Read the survey report at .human/gardening/.gardening-survey.md and all candidate findings from the candidates file at <candidates path from `human pipeline init gardening`>. Validate every finding against actual code, assess compound impact, compute health scorecard grades, and write the final gardening report to the path from `human pipeline report gardening`")
```

## Phase 4: Create Ticket

After the triage report is complete:

1. Read the final gardening report from `.human/gardening/gardening-*.md` (the one just written by triage).
2. Resolve the destination with `human tracker topology`: in split topology the gardening ticket belongs on the `engineering` tracker; in single mode (no `engineering` entry in the output) it goes on the `pm` tracker, which carries the whole ticket lifecycle. If the project is ambiguous, ask the user via `AskUserQuestion`: "Which tracker and project should the gardening ticket be created on? (e.g., 'linear --project=HUM' or 'github --project=myorg/myrepo')"
3. Create the ticket with:
   - **Title**: "Codebase gardening: <N> findings (<health summary>)" — e.g., "Codebase gardening: 12 findings (3 high, 5 medium, 4 low)"
   - **Description**: The full gardening report content (health scorecard, findings, fix plans, recommended order)

```bash
human <tracker> issue create --project=<PROJECT> "<title>" --description "$(cat .human/gardening/<report-file>.md)"
```

The ticket key is returned by the command. This ticket can then be executed with `/human-execute <KEY>`.

## Phase 5: Fix (optional)

After the ticket is created, present the user with three choices:

- **(A) Apply all high-impact fixes**: For each high-impact finding, in the order recommended by the triage report: run `make test` before, apply the refactoring, run `make test` after, run `make lint`, and create an atomic commit referencing the finding. Revert the change if tests fail.
- **(B) Choose individual fixes**: Present the numbered list of findings. The user picks which ones to fix. Apply each chosen fix using the same test-before/refactor/test-after/lint/commit cycle.
- **(C) Skip fixes**: No fixes applied now. The user can run `/human-execute <KEY>` later to execute the fix plan from the ticket.

## After completion

Tell the user:
- The overall health scorecard grades (A-F per dimension)
- How many findings were identified (by impact level)
- The path to the final report
- The ticket key (for `/human-execute`)
- Any critical structural issues that need immediate attention
- If fixes were applied, how many succeeded and how many were reverted
