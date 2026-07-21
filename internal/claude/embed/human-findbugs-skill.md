---
name: human-findbugs
description: Scan the codebase for bugs using an iterative multi-agent pipeline
---

# AI-Powered Bug Scanner

Scan this codebase for bugs using an iterative agent pipeline: reconnaissance once, then repeated deep analysis passes that accumulate candidate bugs, followed by triage when no new bugs are found.

## Phase 1: Reconnaissance

Initialize the pipeline workspace, then run the recon agent. Set the status to
`running` immediately so the desktop Bugs pane's hunt indicator lights up during
recon, before the iterative analysis phase:

```bash
human pipeline init bugs
human pipeline state set bugs status running
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
Task(subagent_type="findbugs-triage", prompt="Read all candidate findings from .human/bugs/.bugs-candidates.md and the recon report from .human/bugs/.findbugs-recon.md. Validate each finding against the actual code, deduplicate, assign final severity, and write the final report to the path printed by `human pipeline report bugs`. Do NOT clean up — the skill files tickets from your report first and cleans up as its final step.")
```

## Phase 5: File confirmed bugs as tickets

Every finding that survived triage becomes a bug ticket so it appears in the
Bugs pane for triage or an autonomous fix run. Do this only for findings the
triage report kept (never for the excluded false positives).

1. Resolve the PM tracker and project:

    ```bash
    human tracker topology
    ```

   Read `pm.type` (the tracker) and `pm.project` (the project). The board runs a
   single project, so `pm.project` is unambiguous.

2. Read the final report written by triage (the path from `human pipeline report bugs`).

3. For EACH finding in the report, in report order (Critical → High → Medium →
   Low; the report is already ranked by severity and confidence), file one bug
   ticket:

    ```bash
    human <pm.type> issue create --type Bug --project=<pm.project> \
      "<finding title>" \
      --description "$(cat <<'BUG_EOF'
    **Severity**: <severity> · **Confidence**: <confidence>
    **Location**: <file>:<line> (<category>)

    <description>

    **Impact**: <impact>

    **Evidence**:
    ```
    <evidence>
    ```

    **Suggested fix**:
    ```
    <suggested fix>
    ```

    _Filed automatically by the Findbugs sweep._
    BUG_EOF
    )"
    ```

   `--type Bug` maps to the tracker's native defect marker, so each ticket lands
   in the Bugs pane exactly like a hand-filed bug.

4. If the report contains **zero** surviving findings, file nothing — the pane's
   "No open bugs" state is the correct "nothing found" outcome.

## Phase 6: Clean up

After all tickets are filed, remove the sweep's intermediate files (this also
clears the hunt indicator). The timestamped report is kept.

    ```bash
    human pipeline cleanup bugs
    ```

## After completion

Tell the user:
- How many iterations ran before convergence
- How many bugs were found (by severity)
- How many bug tickets were filed
- The path to the final report
- Any critical findings that need immediate attention
