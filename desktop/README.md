# Workflow Board (desktop)

A native cross-platform desktop app ([Wails v2](https://wails.io)) presenting the
delivery pipeline as five columns plus a terminal Deploy drop zone. Each card
is a ticket; dragging a card forward triggers that stage's `human` action through
the running daemon. Placement, badges and running/error state all derive from
the `[human:…]` comment markers (and, for ideas, the `human/idea` label) the
daemon ships on the wire — the frontend never re-derives a stage.

## What it does

- Renders five queues — Ideas → Product backlog → Engineering backlog → Code → Ready to Deploy — whose names are true of every card in them, plus the slim **Deploy** drop zone at the right edge. The Ideas queue is an **idea space** — one rounded rectangle five invisible lanes wide: drag ideas left or right to sort looser ones left and concrete ones right. Placement is a local workspace preference saved to `~/.human/ideaspace.json` — never a label, comment, or status on the ticket — and new ideas start leftmost. Code holds the whole build-and-review cycle: dropping an engineering-backlog card onto it launches the executor, and when the handoff posts the daemon chains straight into the reviewer — no gesture. A passing review releases the card into Ready to Deploy by itself; a failing verdict pins it in Code with a `⚠ review found problems` badge and the findings as a ticket comment. Re-dropping the flagged card onto Code rebuilds it against those findings. A review that ends in a genuine fork posts a `[human:options]` block instead of a dead-end: the card shows a **decision needed** badge, the detail panel renders the choices as buttons, and clicking one records the pick on the ticket and relaunches the named stage with that direction. Verbs live on the drop targets (Define it / Plan it / Build it).
- Dropping a reviewed card on **Deploy** ships it: the daemon pushes the branch, opens the PR, waits for CI to go green, merges, deletes the branch, and closes the ticket — the card leaves the board, which shows only work in flight. On merge-deploy platforms (Scalingo, Heroku, Vercel, …) the drop puts the change in production. Failures (CI red, merge conflict) leave the card in Ready to Deploy with the reason.
- Clicking any card opens a detail panel that surfaces — beyond title/owner/description/tracker-link — the review findings, the failed-stage reason, and the fix summary, all sourced from the ticket's own comments (`[human:review-complete]`, the latest `*-failed` marker, `[human:fix-summary]`), never from local files, so it reads the same on every machine and tracker backend.
- Right-click on any card opens a context menu: *Open in tracker* and *Close ticket* (the rare escape hatch for abandoned work — shipped tickets close themselves). Product-backlog cards additionally offer *Create mocks*, which runs the `/human-mockups` skill for the ticket in the project devcontainer; while generation runs the item reads *Creating mocks…*, and once the set exists it becomes *View mocks* and jumps straight to that ticket's set in the Mockups view. The ticket→set link lives in the project's `.human/mockups.json` — never on the tracker.
- The bug rail item opens the **Bugs pane**: defect tickets (bug label or bug issue type) live here instead of the board's columns. A wide grid on the left holds every open bug — five rows tall, cards flowing horizontally — next to a red-bordered **Fix** activity column (the defect counterpart to Code's green). A **+** next to the Bugs header opens a file-a-bug dialog (title + description); the new card appears in the grid immediately with a spinner until the tracker confirms, and the ticket is bug-marked the way the PM tracker natively understands it. Dropping a bug onto Fix launches the autonomous `/human-autofix` pipeline (triage → plan → test-first fix → review handoff; no planning gate, autofix plans itself), the daemon chains the review exactly as it does for builds, and a passing verdict marks the card `fixed ✓`. The **Deploy** button on the right ships every fixed bug through the same push → PR → CI gate → merge pipeline as the board's Deploy drop.
- The stats rail item opens the **Stats view**: a glanceable page of what the factory is doing with its resources, built entirely from data `human` already records locally (no new collection, nothing leaves the machine). A headline row (current-window token usage, tool calls, audit outcomes, agent runs — each with its success/failure or fresh/cache split) sits above four panels: **Tokens per hour** (fresh next to cache-read), **Tool calls by tool**, **Audit outcomes by day** (approved/denied/failed), and **Network decisions** (a live snapshot with a `live` badge). A 24h / 7d / 30d switch drives every panel except the range-exempt live network view; a per-panel empty state and a "history still filling" note (when the daemon started more recently than the selected range) keep an empty source legible. The whole payload comes from one daemon route (`stats-overview` → `App.Stats`), so the range switch is atomic across panels.
- The idea space's `+` quick-captures a title-only ticket carrying the `human/idea` label into the leftmost sub-column. Dragging an idea onto Backlog opens guided ideation in evolve mode: the finished draft rewrites the same ticket in place — title and description replaced, idea label removed, key preserved — instead of creating a new one.
- Card badges: a spinner while an agent runs (planning… / building… / reviewing… / deploying…), an error badge on failure, a warning on a failing review verdict.
- Drag a card to its single next column to advance it — earlier or non-adjacent targets reject and snap back; no backward target is offered (except the rework re-drop onto Code). The drag is the consent; there is no secondary confirmation.
- Optimistic move on drop, then reconcile from the daemon (which is authoritative: it re-derives the card from live comments and enforces forward-only/gated rules server-side — including blocking a deploy on a failing review verdict).
- When Docker is unavailable the agent-launching drop targets are disabled (Deploy stays enabled, since it launches no agent).
- Live updates: subscribes to the daemon and refetches board cards on every change event. A small independent poll (every 3s) drives only the header daemon-reachability dot, since there is no daemon-pushed event to subscribe to when the daemon itself is down — this mirrors how the TUI itself layers periodic ticks on top of its daemon subscribe channel.
- Header daemon indicator: a two-state dot (reachable/unreachable) in the header, sourced from `daemon.ReadInfo()` / `IsReachable()` / `ReadAlivePid()` — display-only, no daemon version, proxy stats, agent count, or start/stop action.
- Two visual styles, toggled with **F8** and persisted across restarts: the default calm style, and a demo-oriented "fancy" style (animated gradient, per-column pastel hues, fireworks/confetti drop celebrations — see the `FANCY THEME` section of `frontend/static/style.css` and `frontend/src/fancy.ts`). Classic rendering is untouched when fancy is off; `prefers-reduced-motion` keeps the fancy colors but disables all movement. Closing a ticket is never celebrated.

## Settings

- The gear rail item opens a full settings view over `.humanconfig.yaml`: a section sidebar (project, trackers, knowledge, messaging, vault, daemon — sections appear as configured) with per-field forms. Edits save on blur (✓ flash on success, inline error on rejection) through the daemon's `config-set` route, which rewrites the file with a comment- and order-preserving YAML round-trip — the file stays hand-editable and git-diffable.
- **Ctrl+,** (or the sidebar search box) opens the settings command palette from any view: every config leaf is fuzzy-searchable by dotted path (`linears.work.projects`), current values shown inline; Enter edits the selected key in place.
- Hot-apply vs restart: tracker/knowledge/messaging edits take effect on the next daemon request (config is re-read per request); only `vault.*` and the top-level `project` need a daemon restart, and those fields carry a `restart` badge.
- Secrets are write-only: `1pw://` vault references display verbatim (they are pointers, not secrets), literal tokens display as a masked placeholder the daemon refuses to accept back, so a stored secret can never round-trip out of — or accidentally back into — the file. The visual theme stays outside settings: it is client-side localStorage, toggled with F8.

## Projects

- On launch, the app loads the board for whatever project the daemon is already serving (unchanged). If no daemon is running, it auto-starts the daemon for the last-opened project (recorded locally at `~/.human/recentprojects.json`, `internal/recentprojects`) when that directory still exists; otherwise it shows a **Projects Overview** screen listing up to the 10 most recently opened projects plus an "Open other directory…" picker (native OS dialog or a manual path) for any directory containing a `.humanconfig.yaml`.
- A **Switch Project** control is always visible in the header. Activating it stops the running daemon and returns to the Projects Overview; picking a project stops any running daemon, starts the chosen project's daemon (via the `human` CLI, which must be installed on PATH — see `internal/daemon`'s `ResolveCLIPath`), and reloads the board with no restart.

## Architecture

- `app.go` — the Go backend bound into the webview. It talks ONLY to the daemon client (`daemon.GetTrackerIssues` / `daemon.BoardTransition` / `daemon.Subscribe`); credential handling, PM-role resolution and the destructive-confirm bypass all live in the daemon.
- `main.go` — Wails bootstrap and the daemon subscription bridge.
- `frontend/` — the HTML/TS board. `src/board.ts` is the typed source; `dist/` is the checked-in built output that `//go:embed all:frontend/dist` ships, so the app runs without a separate npm build. `npm run build` regenerates `dist/`.

The whole Go package is behind the `wailsapp` build tag (cgo webview backend), so
`make build` / `make check` on a plain toolchain never touch it.

## Build

See [docs/desktop-app.md](../docs/desktop-app.md). In short:

```bash
make desktop-deps   # pinned Wails CLI
make desktop        # build for the current OS (cannot cross-compile)
make desktop-dev    # live-reload dev loop
```

### Dev-mode PATH gotcha (`human` CLI not found)

`make install` (`go install .`) puts the `human` binary in `$GOBIN`/`$GOPATH/bin`
(e.g. `~/go/bin`), which is only on PATH for shells that source your profile —
GUI apps launched from Finder/Dock/Spotlight inherit macOS's minimal default
PATH instead, so `ResolveCLIPath` (`internal/daemon/lifecycle.go`) fails with
"human CLI not found on PATH" even though `human` works fine from a terminal.

This only affects building `human` from source (development mode). A normal
install via `brew` places the binary directly in `/opt/homebrew/bin` or
`/usr/local/bin`, which GUI apps do see, so this never comes up for installed
users.

Fix for local dev: symlink the built binary into a directory on the default
GUI PATH, e.g.:

```bash
ln -sf "$(go env GOPATH)/bin/human" /opt/homebrew/bin/human
```
