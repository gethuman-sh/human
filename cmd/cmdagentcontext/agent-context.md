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
- `human plan show <KEY>` — print the ticket's engineering plan from its `[human:plan]` comment

## Pull product context
- `human notion search "<query>"` — docs, specs, notes
- `human figma file get <key>` — designs, components, comments
- `human amplitude events list` — product analytics

## Ship
- `human pr create --head <branch> --title "…" --body "…"` — open a PR (forge and repo derived from the git origin remote)
