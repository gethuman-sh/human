---
name: human-idea-draft
description: Draft an idea ticket's PM description in the background, leaving every gap as an inline [TBA: …] instead of guessing
argument-hint: <idea-key>
---

# Overview

Point this skill at a freshly captured idea and it writes that ticket's PM description while the idea sits in the Ideas column — so promotion later opens on a real draft instead of a blank page. It runs **once, in a single pass**, reads the repository for evidence, and never sub-launches other agents.

This skill runs **without user interaction**. Do NOT use `AskUserQuestion` at any step — there is nobody to ask, and that is precisely why the unanswerable parts stay on the page as questions rather than becoming answers.

The artifact is the **description**. The title is the user's own capture phrase and is never touched.

`$KEY` below is the idea key passed as the argument.

## The rules (these govern every sentence you write)

- **Never invent.** No persona, metric, user count, deadline, dependency or constraint outside a `[TBA: …]`. A confident-sounding description whose invented detail is indistinguishable from a real one is worse than a blank page: the user has to detect the fabrication before deleting it.
- **A gap stays where it belongs.** Write `[TBA: <the question>]` inline, in the sentence the assumption would have gone into. No "Open Questions" section, no footnotes, no summary of the unknown at the end — a gap collected elsewhere is a gap the reader has to re-attach to the sentence it came from.
- **The question is the payload.** `[TBA: which team is this for?]`, not `[TBA: unclear]`. A marker that does not ask anything cannot be answered.
- **The repository is evidence; the user is not available.** Read the code for what exists today and state that. Anything about intent, audience, priority or business value is a `[TBA: …]`.
- **The title is not yours.** Never propose one, never edit one.
- **Most of a one-line idea is `[TBA:]`, and that is the correct output.** A draft that is mostly questions has done its job; a draft that is mostly answers it could not have known has failed.

## Procedure

### 1. Announce the run

```bash
human marker post $KEY idea-draft-started
```

### 2. Ask whether your work is wanted at all

```bash
human idea draft $KEY --check
```

Read the JSON `decision`:

- `stand-down` — a human owns this description. Record the stand-down and stop; write nothing:
  ```bash
  human idea draft $KEY --stand-down
  ```
- `current` — the draft is already current and its input has not changed. Record and stop; write nothing.
- `write` — continue.

### 3. Read the ticket

```bash
human get $KEY
```

Use the per-ticket detail fetch — a board card and a list fetch may carry no description at all on some trackers, where an omitted description is indistinguishable from an empty one.

### 4. Read the repository for what exists today

```bash
human codenav search "<the surface the idea names>"
human search "<subject terms>" --json --limit 20
```

What the code already does is evidence you may state. What a search did not find is not evidence that nothing exists — say so as a `[TBA: …]` rather than as a fact.

### 5. Compose the description

Write the full text to a file, in this shape:

- **Problem Statement** — what is wrong or missing today, grounded in the repository where possible.
- **User Story** — as a `[TBA: which user?]`, I want …, so that ….
- **Acceptance Criteria** — checkboxes, each one checkable; every criterion resting on an unknown carries its `[TBA: …]` in the criterion itself.
- **Scope** — what is in, what is deliberately out.

Every point the evidence does not support carries its `[TBA: …]` in place.

### 6. Write it through the guard

```bash
human idea draft $KEY --description-file <file>
```

Read the JSON back:

- `written: false` — a human wrote to the ticket in the meantime. That is a correct outcome, not a failure. Record it and stop.
- `roundtrip_ok: false` — the tracker did not store the `[TBA:` text verbatim. Say so in the state record; the draft is written either way.

### 7. Record the outcome

```bash
human state set <IDEA_KEY> stage.idea-draft --json --body-file - <<'EOF'
{"exit":"done",
 "summary":"one line — what was drafted, or why nothing was",
 "evidence":"the decision and the TBA count the command reported",
 "next":"what a person would do with this"}
EOF
```

## Autonomy and boundaries

- No human gates — draft and record.
- Never edit the title.
- Never add or remove a label; never close, link or transition the ticket.
- Never post a marker other than the two named here.
- The description write goes through `human idea draft` alone — never `human issue edit`, never a direct tracker call. The guard that protects the user's own words lives in that command, and a write that goes around it goes around the guard.

<!-- human:include fsm -->

<!-- human:include exit-contract -->
