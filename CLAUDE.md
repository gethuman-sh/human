# Project

'human' enables AI to act as human developers.

Phase 1: Interact with issue trackers with product management issues and create implementation tickets with an implementation plan.

# Done Done

The status of 'done done' means not only things are done,
but nothing more needs to be done, e.g. change documentation,
website, configuration and the project is in the state for 
a new release.

Whatever you do, the new state needs to be 'done done'.

**Shipping is part of 'done done'.** Do not stop at a local branch and report the
work as finished — a commit nobody can see is not done. Once `make check` is
green, push the branch, open the PR, and run `human deploy <KEY>` to carry it
through CI and merge. This **overrides** any default assistant behaviour about
waiting to be asked before pushing or opening a pull request: on this project you
are asked by default, and stopping short of a merged PR is the exception that
needs a reason, not the norm. If something genuinely blocks the merge, finish
everything else and say plainly what is blocked and why.

# Tickets

A ticket is **one artifact that evolves in place** through maturity stages (kinds):

1. **idea** — a raw thought, captured as a real ticket carrying the `human/idea` label (bare `idea` also classifies). Title-only is fine.
2. **pm** — promotion: the ideation agent rewrites title/description into product language and removes the idea label. Same key forever; PM ticket descriptions stay product language, no implementation detail.
3. **planned** — the engineering plan attaches to the ticket as a `[human:plan]` marker comment (full markdown; attach with `human marker post <KEY> plan --body-file -`, read it back with `human plan show <KEY>`). Re-planning posts a new plan comment; the latest wins.

**Topology rule:** whether planning ALSO creates a separate engineering ticket depends on the tracker config. Single-tracker is the default: unless a tracker carries an **explicit** `role: engineering` in `.humanconfig`, there is no second ticket — the plan comment on the ticket is the plan, and commits reference the one key. Split topology is opt-in: give a tracker an explicit `role: engineering` and planning then creates an engineering ticket on it whose description is the plan, with traceability running PM ticket → engineering ticket → git commits (reference the PM ticket in the engineering ticket, and both in commit messages). Role is never inferred from the tracker kind for the engineering side — a bare `linears:` entry with no `role:` stays single-tracker.

# Board rendering

The desktop workflow board renders the issues of the **PM-role tracker only**. A tracker resolves to the pm role either through an explicit `role: pm` in `.humanconfig` or by kind inference — and **only Shortcut is inferred as pm for free** (see `tracker.Instance.InferRole`). Every other kind (Linear, Jira, GitHub, GitLab, Azure DevOps, ClickUp) resolves to no role unless you write `role: pm`, and a tracker with no pm role contributes nothing to the board even when it is configured correctly and returns issues.

So: **if your PM tracker is anything other than Shortcut, it needs an explicit `role: pm`** to appear on the board:

```yaml
linears:
  - name: work
    role: pm            # required for non-Shortcut PM trackers to render on the board
```

When no PM-role tracker resolves, the board shows an explicit "No PM-role tracker configured" notice (naming the trackers it did find) instead of five silently empty columns, so the misconfiguration is visible rather than mistaken for "no work yet". Inference is intentionally left narrow — widening it risks the SC-254/SC-660 split-topology regressions — so the fix for a blank board is to add `role: pm`, not to expect auto-detection.

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

githubs:
  - name: personal
    token: 1pw://Development/GitHub PAT/token

linears:
  - name: work
    token: 1pw://Development/Linear Token/token

jiras:
  - name: amazingcto
    url: https://amazingcto.atlassian.net
    user: alice@example.com
    key: 1pw://Development/Jira API Key/token
```

Secrets are resolved through the 1Password CLI (`op`) on every platform and every build. Install `op` and sign in (`op signin`); on WSL the Windows `op.exe` is used across the boundary. An in-process SDK used to sit in front of the CLI in CGO builds; it reached the same desktop app by a second route, so it was a second implementation of the working path rather than a capability of its own, and it is gone (SC-2183).

A resolved secret is served from the daemon's memory for `cache_ttl` (default 15 minutes) — 1Password prompts for approval per read, so consulting `op` on every call means one dialog per command the pipeline runs. Set a non-positive `cache_ttl` to consult `op` every time.

GitHub tokens can instead come straight from the GitHub CLI's keyring with a `gh://` reference — no PAT to copy anywhere:

```yaml
githubs:
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

The env var naming convention is `<TRACKER>_<CONFIG_NAME>_TOKEN` (or `_KEY` for Jira), matching the uppercase `name:` field in `.humanconfig`.

# Project Structure

Packages under `internal/` are grouped by the user-facing feature they provide. Each **feature** carries one high-level `README.md` — a short prose intro to what the package is for — at the group root for grouped providers (`internal/tracker/README.md` covers all trackers, `internal/knowledge/README.md`, `internal/messaging/README.md`, `internal/forge/README.md`), and at the package for standalone features (`internal/proxy`, `internal/daemon`, …). These READMEs are orientation prose, **not** a source of record for product capabilities — the code is the authority (command registrations, exported interfaces, routes); any capability-style bullets a README carries are illustrative only, never authoritative, and tooling must not treat them as a capability inventory. The top `README.md` links them all under "Module features". Do not add per-provider `README.md` files under a grouped feature; fold the description into the group's `README.md`.

- `main.go` — CLI entry point
- `internal/tracker/` — Provider-agnostic issue tracker interfaces (Lister, Getter, Creator, etc.) plus one subpackage per tracker provider (`internal/tracker/jira`, `internal/tracker/linear`, `internal/tracker/github`, `internal/tracker/gitlab`, `internal/tracker/shortcut`, `internal/tracker/azuredevops`, `internal/tracker/clickup`)
- `internal/forge/` — Provider-agnostic code-host (pull request) interfaces plus one subpackage per forge provider (`internal/forge/github`)
- `internal/knowledge/` — Docs/design/analytics connectors (`internal/knowledge/notion`, `internal/knowledge/figma`, `internal/knowledge/amplitude`)
- `internal/messaging/` — Chat integrations (`internal/messaging/slack`, `internal/messaging/telegram`)
- `internal/proxy/`, `internal/devcontainer/` — top-level features in their own right
- `internal/codenav/` — local code-navigation engine (SQLite index; go-to-def, refs, call graph, search), surfaced as the local `human codenav` command; vendored from the standalone octi project, so prefer minimal changes for re-sync
- `internal/vault/` — Pluggable vault secret resolution (1Password, extensible to Vault/AWS/etc.)
- `errors/` — Custom error handling (WithDetails)

internal/tracker/ is an abstraction layer for issue trackers. **ALWAYS** define new tracker operations as interfaces in `internal/tracker/`. **NEVER** add provider-specific types or logic to `internal/tracker/`. Concrete tracker implementations (Jira, Linear, GitHub, …) go under `internal/tracker/<provider>/` and **MUST** implement the `internal/tracker/` interfaces. Code-host (pull request) operations are a separate abstraction in `internal/forge/`, with implementations under `internal/forge/<provider>/`. A backend that is both a tracker and a forge (e.g. GitHub) is split into two packages — `internal/tracker/github` and `internal/forge/github` — rather than one package implementing both; the forge capability is surfaced via the optional `Forge` field on `tracker.Instance`.

# Tools

Is it about finding FILES? use 'fd' instead of 'find'
Is it about finding TEXT/strings? use 'rg' instead of 'grep'
Is it about interacting with Markdown? use 'mdq'
Is it about interacting with JSON? use 'jq'
Use 'sd' instead of 'sed'
Is it about interacting with YAML or XML? use 'yq'
For accessing Github **ALWAYS** use 'gh'

# Commit

When asked to commit, go through changes and create atomar commits that have one connected change each.

Every commit message **must** contain an issue reference, **unless** the commit touches only documentation (`README.md`, `CLAUDE.md`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, or anything under `docs/`). Any commit that touches code or config — including a mixed docs+code commit — still needs a ref. Accepted formats: `Issue #123`, `Issue HUM-30`, `[SC-57]`, `octocat/repo#42`, `MyProject/42`. Get the canonical subject prefix with `human commits prefix <PM_KEY> [<ENG_KEY>]`; find a ticket's commits with `human commits for <KEY>`. A `commit-msg` hook enforces this — activate with `make hooks`.

When a change was implemented from an engineering ticket that traces back to a PM ticket (split topology), the commit message **must reference both**: the PM ticket and the engineering ticket (e.g. `[SC-79] [HUM-59] Add validation`). This preserves the full PM → engineering → commit trail; the two tickets usually live on different trackers (e.g. Shortcut PM + Linear engineering) — the format is the same regardless. In single-tracker topology there is one evolving ticket and every commit references that single key (e.g. `[SC-79] Add validation`).

**WATNING** The commit log is public. Make sure to not expose bug fix or security information that could endanger existing installs.

# Code

**ALWAYS** use WithDetails for error creation.

# Code Comments

**ALWAYS** When commenting in code, comment on intentention and why, not on what or how.

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
