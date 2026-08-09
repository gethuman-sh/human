# Project

'human' enables AI to act as human developers.

# Done Done

The status of 'done done' means not only things are done,
but nothing more needs to be done, e.g. change documentation,
website, configuration and the project is in the state for 
a new release.

**Shipping is part of 'done done'.** Do not stop at a local branch and report the
work as finished — a commit nobody can see is not done. Once `make check` is
green, push the branch, open the PR, and run `human deploy <KEY>` to carry it
through CI and merge. This **overrides** any default assistant behaviour about
waiting to be asked before pushing or opening a pull request: on this project you
are asked by default, and stopping short of a merged PR is the exception that
needs a reason, not the norm. If something genuinely blocks the merge, finish
everything else and say plainly what is blocked and why.

# Communication

Write for the reader this project has: an experienced engineer who is also the
product manager. They know the stack, the tracker and the pipeline, so the
preamble, the restatement of the request and the tour of what you just did are
all waste.

- **Answer first.** The finding, the verdict or the result in the opening line;
  evidence after it, by the shortest path.
- **Compress the style, never the substance** — the point of
  [caveman](https://github.com/juliusbrussee/caveman): brain big, mouth small.
  Code, commands, paths, keys, SHAs, error strings and numbers stay byte-for-byte;
  the prose around them is what goes.
- **Report the fact, not the process that produced it.** No narrating your own
  care, deliberation or diligence: not "the one hole I deliberately did not
  close", not "I checked the real exit code rather than trusting the wrapper",
  not "fixed by extracting two stages that were each already one subject". The
  reader wants the state of the code, not an account of how you arrived at it.
- **Cut what the reader can already see.** No summarising a diff they will open,
  no narrating tool calls, no restating their own question back to them.
- **Never cut the limit** — what is still red, what you did not check, what you
  assumed. State it as a fact ("SIGKILL still leaves no record"), never as a
  confession about your own choices.
- Plain engineer prose. Concision is fewer words, not clipped drama: no aphorisms,
  no dramatic fragments ("The risk in flipping."), no rule-of-three flourishes.

Forty-five words that were five:

> gocyclo caught DeployBranch at 18, then 16 — the pre-push gate doing its job, and
> worth saying plainly since the summary line said exit 0 while `make check` had
> actually exited 2. I checked the real exit code rather than trusting the wrapper.

`gocyclo: split DeployBranch, now 16.`

# Tickets

A ticket is **one artifact that evolves in place** — the same key from first thought
to merge. It matures through three forms:

1. **idea** — a raw thought, captured as a real ticket carrying the `human/idea` label (bare `idea` also classifies). Title-only is fine.
2. **pm** — promotion: the ideation agent rewrites title/description into product language and removes the idea label. Same key forever; PM ticket descriptions stay product language, no implementation detail.
3. **planned** — the engineering plan attaches to the ticket as a `[human:plan]` marker comment (full markdown; attach with `human marker post <KEY> plan --body-file -`, read it back with `human plan show <KEY>`). Re-planning posts a new plan comment; the latest wins.

Those three are the ticket's **maturity**, and two neighbouring words are not it: a
tracker's **kind** is its provider (`shortcut`, `linear`, `jira` — the `.humanconfig`
section and `tracker.Instance.Kind`), and a **stage** is what the board is running
(planning, implementation, review, deploy). Maturity says what is attached to the
ticket; stage says what is working on it; kind says where it lives.

**Topology rule:** whether planning ALSO creates a separate engineering ticket depends on the tracker config. Single-tracker is the default: unless a tracker carries an **explicit** `role: engineering` in `.humanconfig`, there is no second ticket — the plan comment on the ticket is the plan, and commits reference the one key. Split topology is opt-in: give a tracker an explicit `role: engineering` and planning then creates an engineering ticket on it whose description is the plan, with traceability running PM ticket → engineering ticket → git commits (reference the PM ticket in the engineering ticket, and both in commit messages). Role is never inferred from the tracker kind for the engineering side — a Linear entry with no `role:` stays single-tracker.

# Board rendering

The desktop workflow board renders the issues of the **PM-role tracker only**. A tracker resolves to the pm role either through an explicit `role: pm` in `.humanconfig` or by kind inference — and **only Shortcut is inferred as pm for free** (see `tracker.Instance.InferRole`). Every other kind (Linear, Jira, GitHub, GitLab, Azure DevOps, ClickUp) resolves to no role unless you write `role: pm`, and a tracker with no pm role contributes nothing to the board even when it is configured correctly and returns issues.

So: **if your PM tracker is anything other than Shortcut, it needs an explicit `role: pm`** to appear on the board:

```yaml
trackers:
  - kind: linear
    name: work
    role: pm            # required for non-Shortcut PM trackers to render on the board
```

When a tracker resolves but none of them carries the pm role — or no tracker is configured at all — the board shows an explicit "No PM-role tracker configured" notice (naming the trackers it did find) instead of five silently empty columns, so the misconfiguration is visible rather than mistaken for "no work yet". A tracker that *failed to load* is a different fault and gets a different message: its failure is reported through the board's error banner (`board.ErrorBanner`), and the role notice stays silent, because telling a user whose secret store was unreachable to add `role: pm` sends them to edit a `.humanconfig` that needs no editing (SC-3554). Inference is intentionally left narrow — widening it risks the SC-254/SC-660 split-topology regressions — so the fix for a blank board is to add `role: pm`, not to expect auto-detection.

# Review handoff

When an engineer (human or AI agent) finishes coding an engineering ticket and `human-done` passes, the handoff to a reviewer goes via a structured comment on the **PM ticket**. This is tracker-agnostic (works on every backend `human` supports) and requires no custom tracker status.

Post it with `human handoff post <PM_KEY> [--engineering <KEYS>]` — the command derives the branch (current git branch), the commits (short SHAs referencing the work keys), and the daemon id, then verifies every commit is reachable on the branch before posting. Read it back with `human handoff show <PM_KEY>`. The posted body:

```
[human:ready-for-review]
engineering: HUM-89, HUM-90
branch: main
commits: 2037e40, 64bb370
```

- `engineering:` is comma-separated — one PM ticket can spawn multiple engineering tickets. **Single-tracker topology omits this line entirely**: the review target is the PM ticket the comment sits on.
- `branch:` is the branch the commits live on.
- `commits:` is the short SHAs attributed to the referenced keys (what `human commits for <KEY>` returns).

The `human-executor` agent posts this comment automatically as its final step. A reviewer (today: another user runs `/human-pickup-review <PM_KEY>`; future: daemon polling) reads the binding via `human handoff show`, runs `human-reviewer` against each engineering key (or against the PM key when the `engineering:` line is absent), and posts a `[human:review-complete]` follow-up marker (`human marker post`) on the same PM ticket with the verdict.

Do NOT use a custom tracker status for review signalling — that would require every team to reconfigure their tracker. Comments are the universal primitive.

# Pipeline state machine

`internal/pipelinefsm/pipeline-fsm.json` describes how one item — a ticket — moves through the pipeline: every state, every transition, and for each transition the actor that causes it (user, daemon, or skill), the skill that does the work, the marker that records it, and where in the code or the prompt it is implemented. It describes the system **as it is**.

It lives beside the code that parses it, not under `docs/`, because it is not documentation: the build checks it, the tests replay real ticket histories against it, and the binary carries it. A change to it is a code change and its commit needs an issue reference like any other.

**Read it before you plan.** Any change to the derivation, the launchers, the reconcile passes, the marker vocabulary, the board rendering, or any agent prompt that posts a marker: read the document first and plan the change against it, naming the states and transitions you add, remove or redefine. Planning a pipeline change without reading it is how the same bug gets reintroduced under a new number.

**Update it in the same commit.** A commit that moves the machine but not its description leaves the two to drift, which is the failure the document exists to prevent.

A prompt saying "after X, post Y" is a definition of a transition, exactly as much as the Go code that reads Y. Both belong in the document, and a transition that exists in one but not the other is the bug.

**Check it with `make fsm`.** `cmd/fsmcheck` validates the document as a machine — every `dst` and `src` names a declared state, names are unique, nothing is unreachable, no non-terminal state is a trap with no way out, terminal and `reopenable` agree with the edges that exist, every transition names a declared actor and says where it lives (`where:` for Go, `prompt:` for an agent instruction). Errors fail `make check` via `internal/pipelinefsm`; missing prose is a warning. `make fsm-diagram` draws it as a mermaid state diagram. Whether the document is *true of the code* is a separate question the daemon's own conformance test asks (`internal/daemon/pipeline_fsm_doc_test.go`) — a green `make fsm` means the machine holds together, not that it matches.

**Every state carries its invariants**, and this is the half to read when planning: `holds` (what must be true while an item sits there), `who_may_act`, `stale_when`, and `if_nothing_happens`. The transition table says how an item *moves*; these say what happens when it does not — which is what the stuck-card bugs kept turning out to be, because "nothing happened" has no row in a transition table and so kept being nobody's case. The `constants` block names the real budgets (`StuckRunningGrace` 15m, `DefaultStageRetries` 2, `DefaultPRReviewRounds` 3, `DefaultDeployFixRounds` 2, `OutageWaitBound` 6h) so a plan can cite them instead of guessing.

Running states share one liveness rule per stage, held once in `stage_defaults.rules` and taken by a state's `inherits`; `note` records what is true of that state alone. Write it that way — the rule pasted into seven states needs seven coordinated edits to stay true, which is this document's own failure mode turned inward, and the checker flags a state that restates what it already inherits.

`who_may_act` is cross-checked against the transitions that leave the state: if a `daemon` transition leaves a state whose `who_may_act` says only `user`, that is an error. The two halves are written separately and would otherwise drift silently — and a state claiming only a person can move it, while the machine moves it anyway, is "machine acts, never asks" broken in the description before it breaks in code.

`docs/reaper.md` is its companion along the other axis — prose, so it stays under `docs/`: the FSM document follows one ticket, the reaper document follows one container — every condition under which the machine kills an agent (the zombie sweep, the silence reap, the stuck-running and orphan reconcile passes, close-is-cancellation), what it deliberately spares, what the reap costs the ticket's retry budget, and what artifacts it leaves behind. Same rules: read it before changing the sweep, the idle budgets, or any path that stops an agent, and update it in the same commit.

# Daemon

When the human daemon is running, all CLI commands (except `daemon`, `install` and `init`) are automatically forwarded to it. The daemon holds all tracker credentials on the host — **do NOT set tokens manually when the daemon is running**. Just run `human` commands directly.

The daemon is auto-discovered via `~/.human/daemon.json`. Check with `human daemon status`.

## Agent state and recall are per-project

`~/.human/state.db` (`internal/agentstate`) and the recall search index (`internal/recall`) are keyed by `(project, …)`, not by ticket key alone — two registered projects whose keys collide (e.g. both have a `SC-1`) never share a run's working memory, retry budgets, or stage leases. The project is resolved daemon-side from the ticket key via `ProjectRegistry.EntryForKey` — the same routing the board driver already uses — and injected into a forwarded `human state` command as `HUMAN_STATE_PROJECT`; no prompt names the project. Single-project installs, direct CLI use, and rows written before this existed all resolve to the empty string, the "default project" — so an existing `state.db` migrates in place with no reconfiguration and no visible migration step. The recall index makes the same call: `project` is folded into its dedup identity (`UNIQUE(key, source, project)`) so two projects indexing the same tracker instance and key no longer silently replace each other's entry in search results.

# Tracker Tokens (Daemon Host Setup)

These tokens only need to be set **once on the host where the daemon runs**. They are NOT needed for individual CLI invocations when the daemon is running.

## Preferred: Native vault provider (1Password)

Add a `vault` section and use `1pw://` references directly in `.humanconfig.yaml`:

```yaml
vault:
  provider: 1password
  account: my-account    # 1Password account name (top-left in app sidebar)

trackers:
  - kind: linear
    name: work
    token: 1pw://Development/Linear Token/token

  - kind: jira
    name: amazingcto
    url: https://amazingcto.atlassian.net
    user: alice@example.com
    key: 1pw://Development/Jira API Key/token

# The GitHub PAT that opens pull requests is a FORGE, not a tracker. A
# githubs: (or trackers: kind: github) entry is an issue tracker and is
# listed for issues; put it there only if your issues live on GitHub.
forges:
  - name: personal
    token: 1pw://Development/GitHub PAT/token
```

Secrets are resolved through the 1Password CLI (`op`) on every platform and every build. Install `op` and sign in (`op signin`); on WSL the Windows `op.exe` is used across the boundary. An in-process SDK used to sit in front of the CLI in CGO builds; it reached the same desktop app by a second route, so it was a second implementation of the working path rather than a capability of its own, and it is gone (SC-2183).

A resolved secret is served from the daemon's memory for `cache_ttl` (default 15 minutes) — 1Password prompts for approval per read, so consulting `op` on every call means one dialog per command the pipeline runs. `cache_ttl` is a **sliding idle window**: every read pushes it out again, so a secret the pipeline keeps using never forces another approval, and what retires an entry is idleness rather than age. `cache_max_ttl` (default 24 hours) is the absolute ceiling that sliding may never pass, so "in continuous use" never means "held forever". Set a non-positive `cache_ttl` to consult `op` every time. A read that *fails* is likewise left alone for a doubling interval (30s up to 5m) rather than retried at poll rate, so one unanswered approval prompt cannot turn into a queue of them; it clears automatically the moment a read succeeds.

GitHub tokens can instead come straight from the GitHub CLI's keyring with a `gh://` reference — no PAT to copy anywhere:

```yaml
forges:
  - name: personal
    token: gh://token          # gh auth token
  # token: gh://ghe.corp.com/token   # specific host (GitHub Enterprise)
```

`gh://` resolves under any configured vault provider (and with `provider: github` on its own), so 1Password and gh references mix freely.

## Alternative: Environment variables

Tracker API tokens can also be injected via env vars (legacy approach):

```sh
export SHORTCUT_HUMAN_TOKEN="$(op.exe item get 'Shortcut Token' --fields label=notesPlain)"
export LINEAR_WORK_TOKEN="$(op.exe item get 'Linear Token' --fields label=notesPlain)"
export JIRA_AMAZINGCTO_KEY="$(op.exe item get 'Jira API Key' --fields label=notesPlain)"
export GITLAB_HUMAN_TOKEN="$(op.exe item get 'Gitlab Token' --fields label=notesPlain)"
export AZUREDEVOPS_GETHUMAN_TOKEN="$(op.exe item get 'Azure Token' --fields label=notesPlain)"
export TELEGRAM_BOT_TOKEN="$(op.exe item get 'Telegram Token' --fields label=notesPlain)"
```

The env var naming convention is `<KIND>_<CONFIG_NAME>_TOKEN` (or `_KEY` for Jira) — the uppercased tracker kind and the entry's `name:`, which is why the unified `trackers:` shape changed nothing about token names. A `forges:` entry uses `FORGE_<NAME>_TOKEN`.

# Project Structure

Packages under `internal/` are grouped by the user-facing feature they provide. Each **feature** carries one high-level `README.md` — a short prose intro to what the package is for — at the group root for grouped providers (`internal/tracker/README.md` covers all trackers, `internal/knowledge/README.md`, `internal/messaging/README.md`, `internal/forge/README.md`), and at the package for standalone features (`internal/proxy`, `internal/daemon`, …). These READMEs are orientation prose, **not** a source of record for product capabilities — the code is the authority (command registrations, exported interfaces, routes); any capability-style bullets a README carries are illustrative only, never authoritative, and tooling must not treat them as a capability inventory. The top `README.md` links them all under "Module features". Do not add per-provider `README.md` files under a grouped feature; fold the description into the group's `README.md`.

- `main.go` — CLI entry point
- `internal/tracker/` — Provider-agnostic issue tracker interfaces (Lister, Getter, Creator, etc.) plus one subpackage per tracker provider (`internal/tracker/jira`, `internal/tracker/linear`, `internal/tracker/github`, `internal/tracker/gitlab`, `internal/tracker/shortcut`, `internal/tracker/azuredevops`, `internal/tracker/clickup`)
- `internal/forge/` — Provider-agnostic code-host (pull request) interfaces plus one subpackage per forge provider (`internal/forge/github`)
- `internal/knowledge/` — Docs/design/analytics connectors (`internal/knowledge/notion`, `internal/knowledge/figma`, `internal/knowledge/amplitude`)
- `internal/messaging/` — Chat integrations (`internal/messaging/slack`, `internal/messaging/telegram`)
- `internal/proxy/`, `internal/devcontainer/` — top-level features in their own right
- `internal/codenav/` — local code-navigation engine (SQLite index; go-to-def, refs, call graph, search), surfaced as the local `human codenav` command; vendored from the standalone octi project, so prefer minimal changes for re-sync
- `internal/config/` — Reads `.humanconfig` in either shape — the unified `trackers:` list where each entry names its `kind:`, and the older per-vendor sections (`githubs:`, `jiras:`, …) which are still read and always will be. A new config is written unified; `human config migrate --group` converts an existing one on request. Holds the file as one object: `config.Document` parses the whole file, exposes typed `Trackers()`/`Forges()`, mutates through intention-revealing methods, validates itself (including rules spanning two sections), and writes back preserving comments and unknown sections. **A new configuration rule belongs on the Document** (`internal/config/validate.go`), never hand-hung on a provider's loader or buried in a command — and **nothing else may read or write `.humanconfig` directly**: no second yaml parse, no string splicing, no private node plumbing. Those were the three copies that drifted — that is what left the rules with two copies that disagreed ([SC-3889]). `human config check` surfaces them
- `internal/vault/` — Pluggable vault secret resolution (1Password, extensible to Vault/AWS/etc.)
- `errors/` — Custom error handling (WithDetails)

internal/tracker/ is an abstraction layer for issue trackers. **ALWAYS** define new tracker operations as interfaces in `internal/tracker/`. **NEVER** add provider-specific types or logic to `internal/tracker/`. Concrete tracker implementations (Jira, Linear, GitHub, …) go under `internal/tracker/<provider>/` and **MUST** implement the `internal/tracker/` interfaces. Code-host (pull request) operations are a separate abstraction in `internal/forge/`, with implementations under `internal/forge/<provider>/`. A backend that is both a tracker and a forge (e.g. GitHub) is split into two packages — `internal/tracker/github` and `internal/forge/github` — rather than one package implementing both, and the split runs all the way down: each has its own config section (a tracker entry — `trackers:` with `kind: github`, or the legacy `githubs:` — versus `forges:`), its own loader, and its own type (`tracker.Instance` and `forge.Instance`). **NEVER** reunite them by giving one type both capabilities or by asking at a call site which kind a value is — an `IsTracker()`-style predicate is the signature of exactly that mistake, and it cost SC-1671, SC-2132 and SC-3868 before the domains were separated in SC-3876.

# Tools

**Run `human` with no arguments at the start of a session.** It prints the whole command
surface, and that surface is the point: nearly every job here has a command that already
does it properly — reading a ticket, posting a marker, navigating code, opening a PR,
running the deploy gate. Reaching for `gh`, `rg` or a hand-built comment when `human`
has a command for it is how the pipeline's own invariants get bypassed. Check the list
first; the tool changes faster than any description of it, so the help output is the
source of record, not this file.

Is it about finding FILES? use 'fd' instead of 'find'
Is it about finding TEXT/strings? use 'rg' instead of 'grep'
Is it about interacting with Markdown? use 'mdq'
Is it about interacting with JSON? use 'jq'
Use 'sd' instead of 'sed'
Is it about interacting with YAML or XML? use 'yq'
For accessing Github **ALWAYS** use 'gh'

# Worktrees

**Working in the CLI (interactive Claude Code in this repo): ALWAYS work in a git worktree.**
Several Claude Code sessions and pipeline agents run against this checkout at the same time,
so the shared checkout is never yours alone. Editing, committing, stashing or checking out a
branch there means changing files under another run — and its `git status` picks up your
work as if it were its own.

- `git worktree add <path> -b <branch>` before the first edit, one per change.
- `git worktree lock <path>` right after creating it — another session's prune unregisters
  an unlocked worktree mid-run and takes its git metadata with it.
- Never `git add -A` on a directory; stage the files the change actually touches.
- The shared checkout is for reading and for `git pull` only.

# Commit

When asked to commit, go through changes and create atomar commits that have one connected change each.

Every commit message **must** contain an issue reference, **unless** the commit touches only documentation (`README.md`, `CLAUDE.md`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, or anything under `docs/`). Any commit that touches code or config — including a mixed docs+code commit — still needs a ref. Accepted formats: `Issue #123`, `Issue HUM-30`, `[SC-57]`, `octocat/repo#42`, `MyProject/42`. Get the canonical subject prefix with `human commits prefix <PM_KEY> [<ENG_KEY>]`; find a ticket's commits with `human commits for <KEY>`. A `commit-msg` hook enforces this — activate with `make hooks`.

When a change was implemented from an engineering ticket that traces back to a PM ticket (split topology), the commit message **must reference both**: the PM ticket and the engineering ticket (e.g. `[SC-79] [HUM-59] Add validation`). This preserves the full PM → engineering → commit trail; the two tickets usually live on different trackers (e.g. Shortcut PM + Linear engineering) — the format is the same regardless. In single-tracker topology there is one evolving ticket and every commit references that single key (e.g. `[SC-79] Add validation`).

**WATNING** The commit log is public. Make sure to not expose bug fix or security information that could endanger existing installs.

# Code

**ALWAYS** use WithDetails for error creation.

# Code Comments

**ALWAYS** When commenting in code, comment on intentention and why, not on what or how.

# Dependents

Before changing a shared thing, enumerate what depends on it — and scope the
query to the **kind** of thing, not to the call graph. Callers of a Go symbol are
one kind of dependent; the ones that break in practice have no symbol and no call
edge:

| Kind | Query |
|---|---|
| function/type | `human codenav impact <qname> --depth 2` (never `--diff` — it needs a local index a container does not have) |
| closed set of values | `rg -n '<literal>'` across the code **and** `rg -n '<literal>' internal/claude/embed/` |
| stored format | every reader, tests included — and check whether any reads by position rather than by name |
| instruction/convention | `rg -n '<distinctive phrase>' internal/claude/embed/` — the sibling prompts saying the same thing |

Each dependent gets a disposition — examined-and-unchanged or
examined-and-changed — and a kind whose query cannot be run is recorded as
`unchecked`, never left silent. The shipped agents carry this as the shared
`internal/claude/embed/shared/dependents.md` fragment; do not paraphrase it into
a prompt, include it.

# Process

Use todo list as much as possible.

# Release

By default increase versions for a release by 0.1.0

# Verification
 
Run 'make test' before and after changes. Run 'make lint' after changes. **ALWAYS** run 'make check' before pushing.

Treat tests as a second source of truth. **ALWAYS** check for failing tests if the code is wrong or the test is wrong. Fix accordingly. Testcoverage is not allowed to fall below 80%.

Apply these refactorings after changes to keep code testable:
- 'Extract Interface': Accept interfaces instead of concrete types if possible.
- 'Inject Dependencies': Pass dependencies as function/constructor parameters instead of creating them internally.
- 'Extract Function': Pull out logic that is hard to reach via the outer function's inputs into its own function.
- 'Decompose Conditional': Replace IF conditionals and nested IFs with clear, named conditions or early returns.
