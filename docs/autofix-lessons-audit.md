# Autofix lessons vs. the feature pipeline

Every bug the autofix pipeline has fixed is a way this machine can fail. Most of
those failures are not specific to bugs — the fix simply landed wherever the
failure was first seen. This audit walks the whole autofix record and asks, of
each ticket: **would this lesson also solve a feature-pipeline problem, and is it
already applied there?**

**Corpus:** all 84 `autofix/*` merges on `main` (2026-07-15 → 2026-07-31), 171
commits, 84 tickets.
**Feature pipeline:** ideate → ticket-review → plan → implement → review → PR
review/fix loop → deploy.
**Method:** read each commit body for the lesson, then check the code or the
shipped prompt for whether the feature path carries it. Regenerate the corpus
with, for each autofix merge `m`: `git log --pretty="=== %h%n%B" $m^1..$m^2`.

A ticket is **not** a gap when its fix lives in code that already serves every
stage — the board derivation, the failure watcher, the claim arbiter, the zombie
sweep, the deploy pipeline, the vault, the CLI. Most of the record is that.
Gaps cluster where a fix was written into a **prompt**, because a prompt is
per-agent and nothing forces the sibling agents to be updated with it.

---

## Open items

Not yet applied to the feature pipeline. Each is verified missing on `main`, not
inferred.

- [ ] **The PR-loop reviewer can block a better implementation for "diverging
  from the plan."** `human-pr-reviewer-agent.md` step 2 reads the plan "for
  intent" and step 6 blocks with `changes-requested` when the diff "diverges
  from the ticket" — with nothing saying that the outcome is the criterion and
  the plan's mechanism is not. This is the SC-1881 defect, and it is worse here
  than in the standalone reviewer: this reviewer runs in a **loop with a fixer**,
  so the finding is dispatched automatically, the fixer is told to address every
  finding, and a pass that changes nothing trips the convergence guard and reds
  the card. An implementation that found a better route than the plan can burn
  the loop budget being argued back to the plan. *Fix: carry the same
  outcome-over-mechanism paragraph the standalone reviewer now has.*

- [ ] **The PR fixer and deploy fixer still hardcode this repo's Makefile
  gate.** SC-1793 rewrote `human-bug-fixer` and `human-bug-verify` to *detect*
  the project's gate — probe a `Makefile`, then per-ecosystem runners, skip what
  is absent — because a project without a Makefile got instructions it could not
  follow. `human-pr-fixer-agent.md` step 4 and `human-deploy-fixer-agent.md`
  step 3 still say `make test` / `make lint` / `make check` outright. Both agents
  run on **every** card: `openDraftPRAndReview` opens the draft PR and starts the
  review/fix loop for any Deploy transition, feature or bug. *Fix: apply the
  SC-1793 detect-first rewrite to both.*

- [ ] **Verify whether the planner and reviewer need a stage lease.** The
  `stage-lease` fragment is included by `human-bug-fixer`, `human-bug-triage`,
  `human-executor` and `human-security-triage`, but not by `human-planner`,
  `human-reviewer`, `human-pr-*` or `human-deploy-fixer` — all of them
  board-launched stages a second daemon could in principle claim. This may be
  intentional: the daemon's cross-daemon claim arbiter is the real guard and the
  prompt-level lease is belt-and-braces whose fixed TTL is already known to be
  wrong in both directions (SC-1262). *Decide it deliberately and record the
  answer, rather than leaving the split looking accidental.*

---

## Applied during this audit

- **SC-1881 — outcome is the criterion, not the mechanism.** Was scoped to bug
  tickets; on the feature path the mechanism is the plan. Generalized in
  `human-reviewer-agent.md`, stated to cover the plan explicitly, locked by a
  prompt-invariant test.
- **SC-267 — a run leaves a plain-language account on the ticket.** Only the fix
  and security pipelines posted `[human:fix-summary]`. Extraction was never
  bug-scoped and the detail pane already rendered it for any card; the executor
  now posts one at every terminal point.
- Two findings that are not autofix lessons but came out of the same read, both
  shipped under SC-2307: infrastructure failures no longer spend the stage retry
  budget, and the executor no longer reads an unreachable tracker as "no plan".

---

## Already covered

### The fix reached the feature path when it was made

| Ticket | Lesson | Where the feature path carries it |
|---|---|---|
| SC-876 | `human get` auto-detection is the only key-resolution path; never infer the tracker from the git remote | applied to nine prompts including executor, planner, reviewer, done, ideator, ready; guarded by a regression test over the embedded markdown |
| SC-1792 | shipped prompts must not cite this repo's own ticket keys | swept across every prompt; `TestEmbeddedPromptsCarryNoTrackerKeys` |
| SC-695 | a review is hard-bound to its handoff branch and commits, and posts only on the dispatched key | reviewer Step 0, both review skills, executor handoff |
| SC-653 | "could not obtain the code" is `unreviewable`, never a `fail` verdict | reviewer + every calling skill; `TestReviewPathPromptsDocumentUnreviewableEscape` |
| SC-1135 | a red suite is re-classified in a clean detached worktree before it blocks | bug-verify, done, reviewer; `TestDoneGatePromptsClassifyRedSuites` |
| SC-454 | planning that finds the work already shipped stops clean, never re-plans | `[human:nothing-to-do]`, plan skill Phase 3a |
| SC-405 | a run with nothing to do is a terminal success, not a crash | `BoardResolved`; executor termination contract case (c) |
| SC-252 | a board run leaves its branch local; the daemon ships it | executor BOARD CONTEXT rule |
| SC-735 | a handoff may not name commits its branch does not contain | `human handoff post` verifies reachability before posting |
| SC-1134 | a commit prefix must be the tracker's canonical key | `tracker.CanonicalCommitKey` in `human commits prefix` |

### The fix lives in code every stage runs through

Board derivation and badges — SC-429, SC-910, SC-1290, SC-1301, SC-1320,
SC-1669, SC-1701, SC-1830, SC-1957, SC-2137. Failure watcher and reconcile —
SC-206, SC-230, SC-341, SC-355, SC-430, SC-878, SC-1136, SC-1419, SC-1450,
SC-1484, SC-1688, SC-1698, SC-2133. Agent lifecycle — SC-201, SC-216, SC-236,
SC-263, SC-411, SC-427, SC-731, SC-1600, SC-2138. Deploy — SC-296, SC-297,
SC-804, SC-911, SC-989, SC-1184. Credentials and trackers — SC-172, SC-177,
SC-254, SC-652, SC-814, SC-879, SC-912, SC-1653, SC-1691, SC-1693, SC-1694,
SC-1959, SC-1996, SC-2042. Desktop and tooling — SC-204, SC-408, SC-428,
SC-609, SC-631, SC-637, SC-671, SC-849, SC-859, SC-1274, SC-1316, SC-1451,
SC-1654, SC-1656, SC-1677. Chores with no lesson — SC-1000 and the gofmt
commits.

SC-1793 is the one ticket in the record that is **half** applied: its
detect-the-project's-gate rewrite reached `human-bug-fixer` and
`human-bug-verify` but not the two fixer agents that serve every card — see the
open items above.

---

## The pattern worth remembering

Three of the four gaps found across this audit and the work that preceded it are
the same shape: **a rule that was right, scoped to the stage that discovered
it.** The record already names the cost — SC-454 shipped the SC-405 defect a
second time on Planning, and its commit says so outright: *"scoping the
ticket-405 carve-out to Implementation is exactly what let the same defect class
ship again on Planning."*

So when a pipeline bug is fixed, the question is not only "is this fixed" but
**"which sibling stages have the same hole"** — and prompts are where to look
first, because shared Go code updates every stage at once and a prompt updates
exactly one agent.
