---
name: human-brainstorm
description: Discover missing features by analyzing the project's code and completed work using a multi-agent pipeline
argument-hint: "[focus-area]"
---

# Missing Feature Discovery

Analyze this project's codebase and completed work to discover missing features using a 4-phase agent pipeline: ask tracker, recon, analysis, and triage.

## Phase 0: Ask Tracker

Ask the user which tracker and project to pull done tickets from using `AskUserQuestion`:

"Which tracker and project should I pull completed tickets from? (e.g., 'linear --project=HUM' or 'shortcut --project=MyTeam'). Type 'skip' to analyze code only without tracker data."

Store the answer as `<tracker-info>`.

## Phase 1: Reconnaissance

Initialize the pipeline runtime, then run the recon agent:

```bash
human pipeline init brainstorms
```

The command creates `.human/brainstorms/` and prints the pipeline paths as JSON — remember the `candidates` path for the triage phase.

```
Task(subagent_type="brainstorm-recon", prompt="Survey this project's codebase and fetch completed tickets. Tracker info from user: <tracker-info>. Focus area (if any): $ARGUMENTS. Write your recon report to .human/brainstorms/.brainstorm-recon.md")
```

Wait for the recon agent to finish before proceeding.

## Phase 2: Analysis (parallel)

Launch all 3 analysis agents **in a single message** so they run in parallel:

```
Task(subagent_type="brainstorm-codebase", prompt="Read the recon report at .human/brainstorms/.brainstorm-recon.md, then analyze the codebase to identify missing features the architecture could support. Append each missing-feature candidate with `human pipeline append brainstorms` and write your context analysis to .human/brainstorms/.brainstorm-codebase.md")

Task(subagent_type="brainstorm-trajectory", prompt="Read the recon report at .human/brainstorms/.brainstorm-recon.md, then analyze completed tickets and git history to identify missing features based on development patterns. Append each missing-feature candidate with `human pipeline append brainstorms` and write your context analysis to .human/brainstorms/.brainstorm-trajectory.md")

Task(subagent_type="brainstorm-opportunities", prompt="Read the recon report at .human/brainstorms/.brainstorm-recon.md, then scan for developer signals (TODOs, FIXMEs) and common-pattern gaps to identify missing features. Append each missing-feature candidate with `human pipeline append brainstorms` and write your context analysis to .human/brainstorms/.brainstorm-opportunities.md")
```

Wait for all 3 agents to finish before proceeding.

## Phase 3: Triage

Run the triage agent to deduplicate, merge, and produce the final ranked list:

```
Task(subagent_type="brainstorm-triage", prompt="Read the shared candidates file at <candidates path from pipeline init> and the context reports from .human/brainstorms/.brainstorm-*.md, validate findings against actual code, deduplicate, rank, and write the final missing features report.")
```

## After completion

Tell the user:
- How many missing features were identified (by priority)
- The top 3-5 missing features with one-line descriptions
- The path to the full report
- Suggest: "Run `/human-ideate <feature>` to create a ticket for any of these features."
