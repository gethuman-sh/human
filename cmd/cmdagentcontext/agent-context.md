# Working with `human` in this repository

The `human` CLI is available here. Prefer its tools over ad-hoc approaches.

## Navigate code — use this instead of grep or reading whole files
When a `human` daemon is running it serves a shared, always-fresh code index — no `index` step, works the same on the host, in any worktree, and inside any agent container. Just query:
- `human codenav def <name>` — go-to-definition (`--outline` for signature + location only)
- `human codenav refs <name>` — find references (with enclosing symbol + line)
- `human codenav callers <qname>` / `callees <qname>` — call graph
- `human codenav callpath --from A --to B` — concrete call paths
- `human codenav impact <qname>` (or `--diff`) — blast radius of a change
- `human codenav search <query>` — full-text search (`--symbols` for names)
- `human codenav overview` / `outline <file>` — cold-start a codebase

If a codenav query says the repo is not indexed, the daemon is still building the shared index — retry shortly (or, with no daemon, run `human codenav index .`); do not fall back to grep.

## Read and track work
- `human get <KEY>` — fetch an issue (auto-detects the tracker from the key)
- `human list` / `human search "<query>"` — list or search issues across trackers
- `human <tracker> issue create|edit|status|comment …` — create and update tickets (a separate engineering ticket in split topology; otherwise the one evolving ticket carries idea, plan, and review)
- `human tracker topology` — which tracker is PM, which is engineering, single vs split; never re-derive this from the tracker list
- `human done <KEY>` / `human close <KEY>` — finish or close a ticket without knowing the workflow's status names
- `human plan show <KEY>` — print the ticket's engineering plan; attach one with `human marker post <KEY> plan --body-file -`

## Pipeline protocol — use these instead of hand-building comments or git incantations
- `human marker post|show|list <KEY> [TYPE]` — post/read the structured `[human:*]` handoff comments (plan, review verdicts, deploy results); validated, latest-wins
- `human handoff post <KEY>` / `handoff show <KEY>` — the ready-for-review handoff; post derives branch/commits/daemon and verifies the commits are pushed
- `human commits for <KEY>` — the commits referencing a ticket; `human commits prefix <PM> [<ENG>]` — the canonical commit-subject prefix

## Pull product context
- `human notion search "<query>"` — docs, specs, notes
- `human figma file get <key>` — designs, components, comments
- `human amplitude events list` — product analytics

## Ship
- `human pr create --head <branch> --title "…" --body "…"` — open a PR (forge and repo derived from the git origin remote)
- `human deploy <KEY>` — the whole deploy gate: PR, CI wait, rebase if stale, merge, markers, ticket close; a branch already merged into the base is a clean success
