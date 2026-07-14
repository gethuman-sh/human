# Workflow Board (desktop)

A native cross-platform desktop app ([Wails v2](https://wails.io)) presenting the
delivery pipeline as six columns. Each card is a ticket;
dragging a card forward one column triggers that stage's `human` action through
the running daemon. Placement, checkmarks and running/error state all derive from
the `[human:…]` comment markers (and, for ideas, the `human/idea` label) the
daemon ships on the wire — the frontend never re-derives a stage.

## What it does

- Renders six columns — Ideas → Product backlog → Engineering backlog → Building → Ready for review → Ready to deploy — whose names are true of every card in them. Building is the one activity lane: dropping an engineering-backlog card onto it launches the executor, the card lives in Building while the build runs (failures stay there, red), and it moves to Ready for review by itself when the handoff posts — Ready for review accepts no drops, cards can only earn their way in. Other agent stages (planning, reviewing) keep the card in its origin queue with a live badge until they complete. Verbs live on the drop targets (Define it / Plan it / Build it / Review it); opening the pull request is a button on reviewed cards.
- The Ideas column's `+` quick-captures a title-only ticket carrying the `human/idea` label. Dragging an idea onto Backlog opens guided ideation in evolve mode: the finished draft rewrites the same ticket in place — title and description replaced, idea label removed, key preserved — instead of creating a new one.
- Card badges: a checkmark when a stage is done, a spinner while an agent runs it, an error badge on failure.
- Drag a card to its single next column to advance it — earlier or non-adjacent targets reject and snap back; no backward target is offered. The drag is the consent; there is no secondary confirmation.
- The Done column uses a guarded inner drop zone (not the whole column) so a stray drop cannot push a pull request.
- Optimistic move on drop, then reconcile from the daemon (which is authoritative: it re-derives the card from live comments and enforces forward-only/gated rules server-side).
- When Docker is unavailable the planning/implementation/verification drop targets are disabled (Done stays enabled, since it only pushes a branch and opens a PR).
- Live updates: subscribes to the daemon and refetches board cards on every change event. A small independent poll (every 3s) drives only the header daemon-reachability dot, since there is no daemon-pushed event to subscribe to when the daemon itself is down — this mirrors how the TUI itself layers periodic ticks on top of its daemon subscribe channel.
- Header daemon indicator: a two-state dot (reachable/unreachable) in the header, sourced from `daemon.ReadInfo()` / `IsReachable()` / `ReadAlivePid()` — display-only, no daemon version, proxy stats, agent count, or start/stop action.
- Two visual styles, toggled with **F8** and persisted across restarts: the default calm style, and a demo-oriented "fancy" style (animated gradient, per-column pastel hues, fireworks/confetti drop celebrations — see the `FANCY THEME` section of `frontend/static/style.css` and `frontend/src/fancy.ts`). Classic rendering is untouched when fancy is off; `prefers-reduced-motion` keeps the fancy colors but disables all movement. Closing a ticket is never celebrated.

## Settings

- The gear rail item opens a full settings view over `.humanconfig.yaml`: a section sidebar (project, trackers, knowledge, messaging, vault, daemon — sections appear as configured) with per-field forms. Edits save on blur (✓ flash on success, inline error on rejection) through the daemon's `config-set` route, which rewrites the file with a comment- and order-preserving YAML round-trip — the file stays hand-editable and git-diffable.
- **Ctrl+,** (or the sidebar search box) opens the settings command palette from any view: every config leaf is fuzzy-searchable by dotted path (`linears.work.projects`), current values shown inline; Enter edits the selected key in place.
- Hot-apply vs restart: tracker/knowledge/messaging edits take effect on the next daemon request (config is re-read per request); only `vault.*` and the top-level `project` need a daemon restart, and those fields carry a `restart` badge.
- Secrets are write-only: `1pw://` vault references display verbatim (they are pointers, not secrets), literal tokens display as a masked placeholder the daemon refuses to accept back, so a stored secret can never round-trip out of — or accidentally back into — the file. The visual theme stays outside settings: it is client-side localStorage, toggled with F8.

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
