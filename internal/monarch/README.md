# Monarch Console

A team-level operations console for a swarm of `human` daemons. Each daemon
opts in by pointing at one monarch address and best-effort-streams what it is
working on — work, burn, capacity — so a lead or CTO gets a live view of the
swarm. Monarch shows *what* is being worked on, never *who*.

## Capabilities

- **Work board** — the tickets currently in flight, each with its state
  (planning / coding / blocked / stopped), the repo, and the anonymous daemon
  holding it.
- **Burn** — token usage for the day, broken down by ticket and by repo. (Wired
  but reported as zero in this first cut; the host daemon cannot yet read
  per-container token counts from the hook stream — see "Boundaries".)
- **Capacity** — how many daemons are connected and how many are busy / blocked
  / idle, derived from each daemon's latest reported state.
- **Newline-delimited JSON over plain TCP** — daemons stream `agent.start`,
  `agent.stop`, and `tokens.used` events to the monarch ingest port (default
  `19290`). No auth (MVP).
- **Best-effort sender** — a bounded ring buffer (drop-**oldest** when full) with
  async reconnect and backoff. A monarch outage only drops events; it never
  slows or blocks daemon work.
- **SQLite storage** — events are stored with decomposed, indexed columns and a
  short 14-day retention window (a live console, not an accountability trail),
  pruned automatically.
- **Standalone `monarch` binary** — a separate binary from `human` (a team runs
  one monarch; each developer runs a daemon). Default mode runs the TCP ingest
  server plus a Bubble Tea TUI (work board + burn panes, capacity header)
  against the local monarch store. `monarch --headless` runs the ingest server
  only, with no TUI, for running as a systemd service.
- **Systemd-hardened headless mode** — under `--headless` the server sends
  `sd_notify` readiness, answers the systemd watchdog, and watches its own
  executable on disk: when the binary is replaced (a new release is deployed) it
  exits cleanly so systemd restarts it on the new binary. `sd_notify` is a no-op
  when not run under systemd, so the mode is safe to run anywhere.

## Privacy

Privacy is a first-class constraint, enforced by construction:

- **No person identity ever leaves the daemon.** The event struct has no name,
  email, or host field. Repo, ticket, and state describe *work*, not people.
- **Each daemon is an opaque stable instance id only** (`daemon-<hex>`),
  generated from random bytes and persisted to `~/.human/monarch-id`. It is
  never derived from hostname, user, or the secret daemon token.
- **Agent ids are opaque too** — `agent-<hash>` derived from the tool-assigned
  agent name and session id, never a person.
- **Opt-in by design.** With no `--monarch` flag and no `monarch:` config block,
  the daemon behaves exactly as before: no id file is created and nothing is
  streamed.

## Boundaries

- `tokens.used` is wired end-to-end (event, transport, store column, burn pane)
  but carries zero counts for now: the host daemon sees only the hook stream,
  which does not yet include token totals. The burn pane renders `—` until that
  source is threaded through.
- `branch` and `ticket` may be empty for ad-hoc sessions not launched via
  `human agent`; the board still shows repo, state, and the anonymous daemon.
- No auth, alerting, budgets, or collision detection — deliberately out of scope
  for this console.
