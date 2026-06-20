# Working with `human` in this repository

The `human` CLI is available here. Prefer its tools over ad-hoc approaches.

## Navigate code — use this instead of grep or reading whole files
Run `human codenav index .` once per repo, then:
- `human codenav def <name>` — go-to-definition (`--outline` for signature + location only)
- `human codenav refs <name>` — find references (with enclosing symbol + line)
- `human codenav callers <qname>` / `callees <qname>` — call graph
- `human codenav callpath --from A --to B` — concrete call paths
- `human codenav impact <qname>` (or `--diff`) — blast radius of a change
- `human codenav search <query>` — full-text search (`--symbols` for names)
- `human codenav overview` / `outline <file>` — cold-start a codebase

If a codenav command errors, run `human codenav <sub> --help` and retry — do not fall back to grep.

## Read and track work
- `human get <KEY>` — fetch an issue (auto-detects the tracker from the key)
- `human list` / `human search "<query>"` — list or search issues across trackers
- `human <tracker> issue create|edit|status|comment …` — create and update engineering tickets

## Pull product context
- `human notion search "<query>"` — docs, specs, notes
- `human figma file get <key>` — designs, components, comments
- `human amplitude events list` — product analytics

## Ship
- `human pr create --head <branch> --title "…" --body "…"` — open a PR (forge and repo derived from the git origin remote)
