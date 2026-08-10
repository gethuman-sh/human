# Working with `human` in this repository

The `human` CLI is available here. Prefer its tools over ad-hoc approaches.

## Navigate code — use this instead of grep or reading whole files
When a `human` daemon is running it serves a shared, always-fresh code index — no `index` step, works the same on the host, in any worktree, and inside any agent container. Just query:
- `human codenav def <name>` — go-to-definition (`--outline` for signature + location only)
- `human codenav refs <name>` — find references (with enclosing symbol + line)
- `human codenav callers <qname>` / `human codenav callees <qname>` — call graph
- `human codenav callpath --from A --to B` — concrete call paths
- `human codenav impact <qname>` (or `--diff`) — blast radius of a change
- `human codenav search <query>` — full-text search (`--symbols` for names)
- `human codenav overview` / `human codenav outline <file>` — cold-start a codebase

If a codenav query says the repo is not indexed, the daemon is still building the shared index — retry shortly (or, with no daemon, run `human codenav index .`); do not fall back to grep.

## Read and track work
- `human get <KEY>` — fetch an issue (auto-detects the tracker from the key)
- `human list` / `human search "<query>"` — list or search issues across trackers
- `human <tracker> issue create|edit|status|comment …` — create and update tickets (a separate engineering ticket in split topology; otherwise the one evolving ticket carries idea, plan, and review)
- `human tracker topology` — which tracker is PM, which is engineering, single vs split; never re-derive this from the tracker list
- `human done <KEY>` / `human close <KEY>` — finish or close a ticket without knowing the workflow's status names
- `human assign <KEY>` — take ownership of a ticket as the current identity; sets the owner only, so unlike `issue start` it never trips a status-change gate
- `human plan show <KEY>` — print the ticket's engineering plan; attach one with `human marker post <KEY> plan --body-file -`

## Pipeline protocol — use these instead of hand-building comments or git incantations
- `human marker post|show|list <KEY> [TYPE]` — post/read the structured `[human:*]` handoff comments (plan, review verdicts, deploy results); validated, latest-wins
- `human handoff post <KEY>` / `human handoff show <KEY>` — the ready-for-review handoff; post derives branch/commits/daemon and verifies the commits are pushed
- `human commits for <KEY>` — the commits referencing a ticket; `human commits prefix <PM> [<ENG>]` — the canonical commit-subject prefix

## Ask the pipeline what to do next — before you stop and ask a person
The pipeline is a state machine, and `human` answers questions about it:
- `human fsm where <KEY>` — where a ticket is, what must hold there, who may move it, `if_nothing_happens` if nobody does, and every way out. Read `if_nothing_happens` before concluding you are stuck: most states are recovered by the daemon on a timer. Only a way out marked `"yours": true` carries a runnable `command`; the rest are listed so you know who you are waiting for. Posting another actor's marker does not advance the item, it puts it somewhere nothing drove it to
- `human fsm marker <name>` — what a marker records, where posting it moves an item, and the fields it requires
- `human fsm constants` — the real budgets: retries, graces, bounds

Ask with the ticket key you already hold — you do not need to know which state you are in, because that is what `where` tells you. It needs the running daemon, since where an item is depends on its agents' liveness and its spent retries; `marker` and `constants` read the machine compiled into the binary and answer with no daemon and no credentials.

## Pull product context
- `human notion search "<query>"` — docs, specs, notes
- `human figma file get <key>` — designs, components, comments
- `human amplitude events list` — product analytics

## Ship
- `human pr create --head <branch> --title "…" --body "…"` — open a PR (forge and repo derived from the git origin remote)
- `human github pr state --number <N>` — read a pull request's state and check results as JSON (headRef/baseRef/headSHA/mergeable/checks); use this instead of `gh pr view`/`gh pr checks`
- `human deploy <KEY>` — the whole deploy gate: PR, CI wait, rebase if stale, merge, markers, ticket close; a branch already merged into the base is a clean success; it records `[human:deploy-started]` on the ticket before touching the forge (never post that marker yourself), and it refuses — without posting a failure marker or redding the card — while an open `[human:options]` decision waits, unless `--override-decision` is passed
