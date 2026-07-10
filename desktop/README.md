# Workflow Board (desktop)

A native cross-platform desktop app ([Wails v2](https://wails.io)) presenting the
delivery pipeline as an interactive 5-stage board. Each card is a PM ticket;
dragging a card forward one column triggers that stage's `human` action through
the running daemon. Placement, checkmarks and running/error state all derive from
the `[human:…]` comment markers the daemon ships on the wire — the frontend never
re-derives a stage.

## What it does

- Renders five forward-order columns: Backlog → Product planning → Implementation → Verification → Done, with per-column counts.
- Card badges: a checkmark when a stage is done, a spinner while an agent runs it, an error badge on failure.
- Drag a card to its single next column to advance it — earlier or non-adjacent targets reject and snap back; no backward target is offered. The drag is the consent; there is no secondary confirmation.
- The Done column uses a guarded inner drop zone (not the whole column) so a stray drop cannot push a pull request.
- Optimistic move on drop, then reconcile from the daemon (which is authoritative: it re-derives the card from live comments and enforces forward-only/gated rules server-side).
- When Docker is unavailable the planning/implementation/verification drop targets are disabled (Done stays enabled, since it only pushes a branch and opens a PR).
- Live updates: subscribes to the daemon and refetches board cards on every change event. A small independent poll (every 3s) drives only the header daemon-reachability dot, since there is no daemon-pushed event to subscribe to when the daemon itself is down — this mirrors how the TUI itself layers periodic ticks on top of its daemon subscribe channel.
- Header daemon indicator: a two-state dot (reachable/unreachable) in the header, sourced from `daemon.ReadInfo()` / `IsReachable()` / `ReadAlivePid()` — display-only, no daemon version, proxy stats, agent count, or start/stop action.
- Two visual styles, toggled with **F8** and persisted across restarts: the default calm style, and a demo-oriented "fancy" style (animated gradient, per-column pastel hues, fireworks/confetti drop celebrations — see the `FANCY THEME` section of `frontend/static/style.css` and `frontend/src/fancy.ts`). Classic rendering is untouched when fancy is off; `prefers-reduced-motion` keeps the fancy colors but disables all movement. Closing a ticket is never celebrated.

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
