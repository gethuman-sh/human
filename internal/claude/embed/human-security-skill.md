---
name: human-security
description: Scan the codebase for security vulnerabilities using an iterative multi-agent pipeline
---

# AI-Powered Security Scanner

Scan this codebase for security vulnerabilities using an iterative agent pipeline: attack surface mapping once, then repeated specialized scanning passes that accumulate candidate findings, followed by exploitation analysis and triage when no new findings emerge.

## Phase 1: Attack Surface Mapping

Initialize the pipeline (creates `.human/security/` and prints the candidates/state paths), then run the surface mapper:

```bash
human pipeline init security
```

```
Task(subagent_type="security-surface", prompt="Map the attack surface of this codebase. Write your report to .human/security/.security-surface.md")
```

Wait for the surface mapper to finish before proceeding.

## Phase 2: Initialize pipeline state

The candidates file is managed by `human pipeline append` — do not create it by hand. Just seed the shared state:

```bash
human pipeline state set security iterations 0
human pipeline state set security status running
```

## Phase 3: Iterative Specialized Scanning

Repeat the following iteration block. Stop when an iteration finds zero new candidates.

### Iteration step

Determine the iteration number and snapshot the candidate count before the round:

```bash
ITER_NUM=$(human pipeline state get security iterations)
ITER_NUM=$((ITER_NUM + 1))
BEFORE=$(human pipeline count security)
echo "Starting iteration $ITER_NUM with $BEFORE existing candidates"
```

Launch all 5 scanning agents **in a single message** so they run in parallel:

```
Task(subagent_type="security-injection", prompt="Read the attack surface report at .human/security/.security-surface.md and existing candidates at .human/security/.security-candidates.md. This is iteration ITER_NUM. Analyze the codebase for injection and input validation vulnerabilities. Report each new finding via `human pipeline append security` as described in your output format.")

Task(subagent_type="security-auth", prompt="Read the attack surface report at .human/security/.security-surface.md and existing candidates at .human/security/.security-candidates.md. This is iteration ITER_NUM. Analyze the codebase for authentication, authorization, and session management vulnerabilities. Report each new finding via `human pipeline append security` as described in your output format.")

Task(subagent_type="security-secrets", prompt="Read the attack surface report at .human/security/.security-surface.md and existing candidates at .human/security/.security-candidates.md. This is iteration ITER_NUM. Scan the codebase and git history for leaked secrets, hardcoded credentials, and weak cryptography. Report each new finding via `human pipeline append security` as described in your output format.")

Task(subagent_type="security-deps", prompt="Read the attack surface report at .human/security/.security-surface.md and existing candidates at .human/security/.security-candidates.md. This is iteration ITER_NUM. Audit dependencies for known vulnerabilities and supply chain risks. Report each new finding via `human pipeline append security` as described in your output format.")

Task(subagent_type="security-infra", prompt="Read the attack surface report at .human/security/.security-surface.md and existing candidates at .human/security/.security-candidates.md. This is iteration ITER_NUM. Analyze configuration files, Dockerfiles, CI pipelines, and infrastructure settings for security misconfigurations. Report each new finding via `human pipeline append security` as described in your output format.")
```

Wait for all 5 agents to finish.

### Check convergence

Compare the candidate count against the pre-round snapshot (`human pipeline append` deduplicates, so the count only grows for genuinely new findings):

```bash
AFTER=$(human pipeline count security)
NEW_TOTAL=$((AFTER - BEFORE))
echo "Iteration $ITER_NUM: $NEW_TOTAL new candidates. Total: $AFTER"
```

Update the shared state:

```bash
human pipeline state set security iterations $ITER_NUM
human pipeline state set security last_new_candidates $NEW_TOTAL
human pipeline state set security total_candidates $AFTER
```

**Decision point:**
- If `NEW_TOTAL` is **0** → proceed to Phase 4 (Exploitation Analysis)
- If `NEW_TOTAL` is **> 0** → go back to "Iteration step" and repeat

## Phase 4: Exploitation Analysis

Update state:

```bash
human pipeline state set security status chains
```

Run the attack chain agent to connect individual findings into exploitable paths:

```
Task(subagent_type="security-chains", prompt="Read all candidate findings from .human/security/.security-candidates.md and the attack surface map from .human/security/.security-surface.md. Trace data flows to build attack chains that connect individual candidate findings into exploitable paths. Reference candidates by their C-NNN IDs. Write your analysis to .human/security/.security-chains.md")
```

Wait for the chain analysis to finish before proceeding.

## Phase 5: Triage

Update state:

```bash
human pipeline state set security status triaging
```

Run the triage agent to validate, deduplicate, and produce the final report:

```
Task(subagent_type="security-triage", prompt="Read all candidate findings from .human/security/.security-candidates.md, the attack chain analysis from .human/security/.security-chains.md, and the surface map from .human/security/.security-surface.md. Validate every finding against actual code, assign severity, and write the final security report to .human/security/. Clean up intermediate files when done.")
```

## After completion

Tell the user:
- How many iterations ran before convergence
- How many vulnerabilities were found (by severity)
- Any critical findings that need immediate attention
- The path to the final report
- If attack chains were found, highlight the most dangerous one
