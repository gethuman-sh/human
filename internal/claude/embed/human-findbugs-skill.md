---
name: human-findbugs
description: Scan the codebase for bugs using an iterative multi-agent pipeline
---

# AI-Powered Bug Scanner

Scan this codebase for bugs using an iterative agent pipeline: reconnaissance once, then repeated deep analysis passes that accumulate candidate bugs, followed by triage when no new bugs are found.

## Phase 1: Reconnaissance

Initialize the pipeline workspace, then run the recon agent:

```bash
human pipeline init bugs
```

```
Task(subagent_type="findbugs-recon", prompt="Perform reconnaissance on this codebase. Write your recon report to .human/bugs/.findbugs-recon.md")
```

Wait for the recon agent to finish before proceeding.

## Phase 2: Initialize pipeline state

```bash
human pipeline state set bugs iterations 0
human pipeline state set bugs status running
```

Candidates are managed by `human pipeline append` — the analysis agents report findings through it, and it accumulates them in `.human/bugs/.bugs-candidates.md`.

## Phase 3: Iterative Deep Analysis

Repeat the following iteration block. Stop when an iteration finds zero new candidates.

### Iteration step

Read the iteration number and record the candidate count before the round:

```bash
ITER_NUM=$(human pipeline state get bugs iterations)
ITER_NUM=$((ITER_NUM + 1))
BEFORE=$(human pipeline count bugs)
echo "Starting iteration $ITER_NUM (candidates so far: $BEFORE)"
```

Launch all 4 analysis agents **in a single message** so they run in parallel:

```
Task(subagent_type="findbugs-logic", prompt="Read the recon report at .human/bugs/.findbugs-recon.md and existing candidates at .human/bugs/.bugs-candidates.md. This is iteration ITER_NUM. Analyze the codebase for logic bugs. Report each NEW finding via `human pipeline append bugs` as described in your instructions.")

Task(subagent_type="findbugs-errors", prompt="Read the recon report at .human/bugs/.findbugs-recon.md and existing candidates at .human/bugs/.bugs-candidates.md. This is iteration ITER_NUM. Analyze the codebase for error handling bugs. Report each NEW finding via `human pipeline append bugs` as described in your instructions.")

Task(subagent_type="findbugs-concurrency", prompt="Read the recon report at .human/bugs/.findbugs-recon.md and existing candidates at .human/bugs/.bugs-candidates.md. This is iteration ITER_NUM. Analyze the codebase for concurrency bugs. Report each NEW finding via `human pipeline append bugs` as described in your instructions.")

Task(subagent_type="findbugs-api", prompt="Read the recon report at .human/bugs/.findbugs-recon.md and existing candidates at .human/bugs/.bugs-candidates.md. This is iteration ITER_NUM. Analyze the codebase for API and security bugs. Report each NEW finding via `human pipeline append bugs` as described in your instructions.")
```

Wait for all 4 agents to finish.

### Check convergence

Compare the candidate count against the pre-round count and update state:

```bash
AFTER=$(human pipeline count bugs)
NEW_TOTAL=$((AFTER - BEFORE))
human pipeline state set bugs iterations $ITER_NUM
echo "Iteration $ITER_NUM: $NEW_TOTAL new candidates. Total: $AFTER"
```

**Decision point:**
- If `NEW_TOTAL` is **0** → proceed to Phase 4 (Triage)
- If `NEW_TOTAL` is **> 0** → go back to "Iteration step" and repeat

## Phase 4: Triage

Update state to triaging:

```bash
human pipeline state set bugs status triaging
```

Run the triage agent to validate, deduplicate, and produce the final report:

```
Task(subagent_type="findbugs-triage", prompt="Read all candidate findings from .human/bugs/.bugs-candidates.md and the recon report from .human/bugs/.findbugs-recon.md. Validate each finding against the actual code, deduplicate, assign final severity, and write the final report to the path printed by `human pipeline report bugs`. Clean up with `human pipeline cleanup bugs` when done.")
```

## After completion

Tell the user:
- How many iterations ran before convergence
- How many bugs were found (by severity)
- The path to the final report
- Any critical findings that need immediate attention
