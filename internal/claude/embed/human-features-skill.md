---
name: human-features
description: Generate FEATURE.json — a grouped, human-readable map of what this codebase can do — from the code, git history, and tickets
argument-hint: "[since]"
---

# Feature Map Generation

Build `FEATURE.json` at the repository root: a grouped, user-facing catalogue of what this
codebase does, so a reader understands its capabilities at a glance. Two agents do the work —
a recon pass that inventories features from the code, and a synthesis pass that groups them,
maps tickets, marks recent changes, and writes the file.

The optional `$ARGUMENTS` is a **recency override** (e.g. `v0.19.0`, `2026-06-01`, or `30 days`).
When omitted, "recent" means *changed since the latest release tag* (fallback: last 30 days).

## Phase 0: Resolve trackers

Run `human tracker list` to see configured trackers. Note the tracker with `"role": "pm"`
(product tickets, e.g. Shortcut) and the one with `"role": "engineering"` (e.g. Linear) — the
synthesis agent uses them to enrich ticket references. If the command fails or no trackers are
configured, continue anyway: ticket mapping falls back to keys parsed from commit messages.

## Phase 1: Reconnaissance

Create the scratch directory, then run the recon agent:

```bash
mkdir -p .human/features
```

```
Task(subagent_type="features-recon", prompt="Survey this repository and build a feature inventory grounded in the code. Recency override (if any): $ARGUMENTS. Write your inventory to .human/features/.features-inventory.md")
```

Wait for the recon agent to finish before proceeding.

## Phase 2: Synthesis

Run the synthesis agent to group the inventory, map tickets, mark recent features, and write the
file. Pass along the tracker roles resolved in Phase 0 so it can enrich ticket titles.

```
Task(subagent_type="features-synthesis", prompt="Read the inventory at .human/features/.features-inventory.md and the existing FEATURE.json (if present). Produce a capability map framed for the product's actual audience — decide from the positioning whether that is consumers, integrating developers, or operators, and match their language. Infer 3–5 value pillars, organize capabilities as pillar › area › feature (aim for 3 levels), cut internal plumbing that audience wouldn't discuss, consolidate granular integrations into single capabilities, and use the audience's value language. Attach the tickets that created or changed each feature, mark recently changed ones, and write FEATURE.json to the repository root. PM tracker: <pm-tracker-or-none>. Engineering tracker: <eng-tracker-or-none>. Recency override (if any): $ARGUMENTS")
```

## After completion

Tell the user:
- The path (`FEATURE.json`), how many groups and features it holds, and the nesting depth used.
- How many features are marked recent (changed since the recency boundary).
- If this is the `human` repo (or any project with the desktop board), remind them to rebuild the
  desktop frontend so the Features pane picks up the change: `cd desktop/frontend && npm run build`.
