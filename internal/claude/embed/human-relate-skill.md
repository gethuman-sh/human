---
name: human-relate
description: Triage a newly filed bug against existing work — link duplicates and relations, flag possible regressions, create real dependency links for unfinished blockers, and always write a record (including "none found" or "could not complete")
argument-hint: <bug-key>
---

# Overview

Point this skill at a freshly filed bug and it records where that bug sits among the existing work: what it duplicates or relates to, whether it looks like the return of something already closed, what unfinished work it must wait for (linked as a real dependency), and — when nothing was found — that statement. It runs **once, in a single pass**, using the same read/search/link CLI the ticket-reviewer already uses. It never sub-launches other agents.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — decide and record. The verdict is **advisory**: when the bug is later picked up for a fix, the fix pipeline's own triage remains the authority; this record informs it, it does not pre-empt it.

**The record is always written.** A bug with no links must never leave a reader guessing whether the search ran, found nothing, or died halfway. Every run ends with exactly one `[human:related]` marker whose head is `found`, `none`, or `incomplete`.

`$KEY` below is the bug key passed as the argument.

## The rules (these govern every decision)

- **Dependencies are linked for real, not suggested.** A bug that depends on unfinished work is held out of Planning and Implementation until its blocker closes — only a real `--blocks` link achieves that.
- **Direction is fixed: the newly filed bug waits on existing work, never the reverse.** A bug filed a minute ago can never stall something already in flight. The only card whose start it may delay is its own.
- **Never create a link that leaves two tickets waiting on each other.** Check before writing (Step 4). A mutual pair costs two dead cards; two similar bugs filed minutes apart is the realistic way to reach it.
- **Only an open ticket can become a blocker.** A closed one holds nothing and only adds noise.
- **A dependency needs stronger grounds than a relation.** When the evidence only supports "these are related", record a relation, not a dependency.
- **Every dependency states its reasoning on the bug**, one line per blocker, so a wrong one can be removed in one step.
- **A duplicate is never closed automatically.** Link it and state it; leave it open. Closing what someone filed a moment ago reads as the tool losing their report.
- **A match against an already-closed bug is a possible regression, not a duplicate.** It means something different and it is the more valuable finding. Do not create a `--blocks` link against a closed ticket.
- **Nothing is claimed on thin evidence.** A one-line bug with nothing to match against results in `none`, not a speculative link.

## Procedure

### 1. Announce the run

Post the visible progress marker so the bug never looks untouched while the triage runs:

```bash
human marker post $KEY related-started
```

### 2. Read the bug

```bash
human get $KEY
```

Read the title and description. If the bug is a single line with nothing concrete to match against — no error, no named surface, no reproducible behaviour — that is grounds for a `none` verdict (nothing is claimed on thin evidence). Proceed to search anyway, but hold this bar.

### 3. Search for siblings by subject, not wording

Search on the *subject* — the surface, symptom, and component — rather than the exact phrasing of the report:

```bash
human search "<subject terms>" --json --limit 20
human codenav search "<the code surface the bug touches>"
```

Exclude `$KEY` itself from the results. For each candidate that looks relevant, read it (`human get <other>`) so the classification in Step 4 rests on the ticket's real state, not the search snippet.

### 4. Classify each candidate and act

For each relevant candidate, decide which one it is and act:

- **Open bug, same defect → duplicate.** Link it as a plain relation and state it. Never close `$KEY`.
  ```bash
  human link $KEY <other>          # LinkRelated (the default — NO --blocks)
  ```
  Record: `duplicate of <other> (open)`.

- **Closed bug, same defect → possible regression** (NOT a duplicate). Do **not** create a `--blocks` link on a closed ticket. Record: `possible regression of <other> (closed)`.

- **Open work in flight, merely associated → relation.**
  ```bash
  human link $KEY <other>          # LinkRelated
  ```
  Record: `related to <other>`.

- **Open work `$KEY` must WAIT for → dependency.** Only on stronger grounds than a relation, and only when `<other>` is OPEN. Direction is fixed — existing work blocks the new bug:
  ```bash
  human link <other> $KEY --blocks   # <other> must finish before $KEY can start
  ```
  (`human link A B --blocks` means A must finish before B — so `<other>` blocks `$KEY`.)
  Record: `blocked by <other> — <one line of reasoning>`.

  **Cycle guard (check BEFORE writing the blocks link):** run `human get <other>` and inspect its links. If `<other>` is already blocked by `$KEY` (the two-bugs-filed-minutes-apart case), do NOT create the link — it would leave the two waiting on each other. Record a relation instead and note the averted cycle. This mirrors the daemon's own `cycleAmong` guard, which is the backstop; your check is the first line of defence.

Every dependency you create gets its own reasoning line in the record so a wrong one can be removed with a single `human unlink`.

### 5. Write the terminal record — always exactly one `[human:related]` marker

- **Something found or linked:**
  ```bash
  human marker post $KEY related --head found --body-file - <<'EOF'
  ## Related work
  - duplicate of <other> (open) — linked
  - related to <other> — linked
  - possible regression of <other> (closed)
  - blocked by <other> — <reasoning> (dependency linked)

  This record is advisory: the fix pipeline's own triage remains the authority when this bug is picked up.
  EOF
  ```

- **Search ran, nothing matched:**
  ```bash
  human marker post $KEY related --head none
  ```

- **The run could not finish** (a search failed, a link write errored, the container is dying) — name what could not be completed. This head deliberately does NOT set the card's completed-record flag, so the manual "Find related work" menu action still offers a re-run:
  ```bash
  human marker post $KEY related --head incomplete --body-file - <<'EOF'
  Could not complete: <what failed and where it stopped>.
  EOF
  ```

## Autonomy and boundaries

- No human gates — decide and record.
- Never close a ticket.
- Never link in the reverse (new-bug-blocks-existing) direction.
- Never create a `--blocks` link against a closed ticket.
- Never create a link that would leave two tickets waiting on each other.
- The record informs the later fix-pipeline triage; it never pre-empts it.
