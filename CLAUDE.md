# Project

'human' enables AI to act as human developers.

Phase 1: Interact with issue trackers with product management issues and create implementation tickets with an implementation plan.

# Done Done

The status of 'done done' means not only things are done,
but nothing more needs to be done, e.g. change documentation,
website, configuration and the project is in the state for 
a new release.

Whatever you do, the new state needs to be 'done done'.

# Tickets

A ticket is **one artifact that evolves in place** through maturity stages (kinds):

1. **idea** — a raw thought, captured as a real ticket carrying the `human/idea` label (bare `idea` also classifies). Title-only is fine.
2. **pm** — promotion: the ideation agent rewrites title/description into product language and removes the idea label. Same key forever; PM ticket descriptions stay product language, no implementation detail.
3. **planned** — the engineering plan attaches to the ticket as a `[human:plan]` marker comment (full markdown; read it back with `human plan show <KEY>`). Re-planning posts a new plan comment; the latest wins.

**Topology rule:** whether planning ALSO creates a separate engineering ticket depends on the tracker config. In THIS repo's setup — PM tickets on Shortcut (project "Stephan Schmidt's Team", `role: pm`) and engineering tickets on Linear (project "HUM", `role: engineering`) — the trackers are split, so planning creates a Linear engineering ticket whose description is the plan, and traceability runs PM ticket → engineering ticket → git commits (reference the Shortcut ticket in the Linear ticket, and both in commit messages). Teams with a single tracker get no second ticket: the plan comment on the ticket is the plan, and commits reference the one key.

# Review handoff

When an engineer (human or AI agent) finishes coding an engineering ticket and `human-done` passes, the handoff to a reviewer goes via a structured comment on the **PM ticket**. This is tracker-agnostic (works on every backend `human` supports) and requires no custom tracker status.

Handoff comment body, posted on the PM ticket:

```
[human:ready-for-review]
engineering: HUM-89, HUM-90
branch: main
commits: 2037e40, 64bb370
```

- `engineering:` is comma-separated — one PM ticket can spawn multiple engineering tickets. **Single-tracker topology omits this line entirely**: the review target is the PM ticket the comment sits on.
- `branch:` is the branch the commits live on.
- `commits:` is the short SHAs attributed to the referenced keys via `git log --grep=<KEY>`.

The `human-executor` agent posts this comment automatically as its final step. A reviewer (today: another user runs `/human-pickup-review <PM_KEY>`; future: daemon polling) parses the block, runs `human-reviewer` against each engineering key (or against the PM key when the `engineering:` line is absent), and posts a `[human:review-complete]` follow-up comment on the same PM ticket with the verdict.

The TUI marks engineering tickets whose PM ticket carries an unresolved `[human:ready-for-review]` comment with an `(R)` annotation in the tracker view.

Do NOT use a custom tracker status for review signalling — that would require every team to reconfigure their tracker. Comments are the universal primitive.

# Daemon

When the human daemon is running, all CLI commands (except `daemon`, `install`, `init`, `tui`) are automatically forwarded to it. The daemon holds all tracker credentials on the host — **do NOT set tokens manually when the daemon is running**. Just run `human` commands directly.

The daemon is auto-discovered via `~/.human/daemon.json`. Check with `human daemon status`.

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

Secrets are resolved via the 1Password desktop app integration. The 1Password app must be installed and running — it will prompt for biometric or master password authentication. Enable "Integrate with other apps" in 1Password Settings > Developer.

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

Packages under `internal/` are grouped by the user-facing feature they provide. Each **feature** carries one high-level `README.md` (a short intro plus a capability list) — at the group root for grouped providers (`internal/tracker/README.md` covers all trackers, `internal/knowledge/README.md`, `internal/messaging/README.md`, `internal/forge/README.md`), and at the package for standalone features (`internal/proxy`, `internal/daemon`, …). The top `README.md` links them all under "Module features". Do not add per-provider `README.md` files under a grouped feature; fold the capability into the group's `README.md`.

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

Every commit message **must** contain an issue reference, **unless** the commit touches only documentation (`README.md`, `CLAUDE.md`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, or anything under `docs/`). Any commit that touches code or config — including a mixed docs+code commit — still needs a ref. Accepted formats: `Issue #123`, `Issue HUM-30`, `[SC-57]`, `octocat/repo#42`, `MyProject/42`. A `commit-msg` hook enforces this — activate with `make hooks`.

When a change was implemented from an engineering ticket that traces back to a PM ticket (split topology, as in this repo), the commit message **must reference both**: the PM ticket and the engineering ticket (e.g. `[SC-79] [HUM-59] Add validation`). This preserves the full PM → engineering → commit trail; the two tickets usually live on different trackers (e.g. Shortcut PM + Linear engineering) — the format is the same regardless. In single-tracker topology there is one evolving ticket and every commit references that single key (e.g. `[SC-79] Add validation`).

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
