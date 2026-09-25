# Daemon ↔ client wire protocol

The daemon and CLI negotiate compatibility with three integers in
`internal/daemon/version_gate.go`, independent of the release version:

- **`Protocol`** — the wire protocol this build speaks. Bumped on **every**
  wire change (new routes, new request fields, changed semantics), additive or
  breaking alike.
- **`MinProtocol`** — the oldest client this daemon still serves. Raised
  **only** for breaking changes. This is the conscious compatibility decision:
  the author of a breaking change bumps it in the same commit and records below
  which clients are cut off. A daemon at protocol 10 with MinProtocol 8 keeps
  serving last month's client at 8.
- **`MinDaemonProtocol`** — the oldest daemon this client accepts. The
  symmetric half: a newer client refuses a too-old daemon with one clear
  "rebuild the daemon" error instead of a bare unknown-command failure. Raised
  only when the client depends on daemon behavior older daemons lack.

Clients that predate the handshake (no `protocol` field in their requests) are
gated by the legacy version-string check (`MinClientVersion`); daemons that
predate it (no `protocol` in `daemon.json`) are accepted by all clients, with
only the version-skew warning.

## Rules for changing the wire

1. Any change to `internal/daemon/protocol.go` request/response shapes, daemon
   routes, or their semantics bumps `Protocol` and adds a ledger line below.
2. If an old client would misbehave (not merely lack a feature), bump
   `MinProtocol` to the new `Protocol` in the same commit and say so in the
   ledger line.
3. If the client now depends on new daemon behavior, bump `MinDaemonProtocol`
   likewise.
4. Never reuse or renumber. The ledger is append-only.

## Where the gate applies

`MinDaemonProtocol` refuses **forwarding**, not running. The decision is made
once, in `main.decideDispatch`, and only after `isLocalSubcommand` has answered
whether the daemon is involved at all:

- Commands that execute in the caller's own process — the whole `daemon` family
  (`start`, `stop`, `restart`, `status`), `doctor`, `--version`, and everything
  else in `main.localSubcommands` — never consult it. They must work against a
  daemon of any protocol: `human daemon restart` is the remedy the refusal
  prints, and a remedy that is itself refused leaves a user with only `kill`
  (SC-5397).
- A help request (`--help`, `-h`, `help`, or a bare `human`) is never refused
  either. Against a daemon below the floor it is answered locally, from the
  binary the user actually invoked — which after an upgrade is the surface they
  need to see.
- Everything else is forwarded, and there — and only there — a daemon below
  `MinDaemonProtocol` stops the run with the one clear restart-the-daemon error.
- A local command that nevertheless sends a request asks the question itself:
  `human hook` (local only so stdin stays available) calls
  `daemon.DaemonProtocolError` in `deliverHookEvent`, and `human doctor` reports
  the refusal as a named, structured failing check — still exiting non-zero,
  same as any other blocking check — rather than the bare unknown-command error
  a forwarded command gets.

`daemon.NewClient` keeps the gate for every caller that sends a request (the
desktop, the proxy, `human audit`, `human stats`). `daemon.NewClientUnchecked`
and `daemon.ConnectUnchecked` exist for the one caller that needs the endpoint
without the permission to use it — the CLI entry point, which also propagates
the chrome and proxy addresses from `daemon.json` for locally-run commands.

## Ledger

| Protocol | Date | Change | MinProtocol | MinDaemonProtocol |
|---|---|---|---|---|
| 1 | 2026-07-21 | Protocol handshake introduced (integer gate on both sides). Pre-protocol clients remain gated by the legacy `MinClientVersion` ≥ 0.21.0 check (last legacy break: the HUM-160 permission-grant cycle). | 1 | 1 |
| 2 | 2026-08-26 | SC-4608 background idea drafting. Added: the `idea-promote` route; `DescEditStartRequest.Promoted`; `BoardCard.TBACount` / `BoardViewCard.TBACount`. Removed: the `ideation-approve` route, `IdeationApproveRequest`, `IdeationStartRequest.EvolveKey`/`EvolveLabels`, `IdeationStatus.Question`/`Draft` (and the `IdeationQuestion`/`IdeationDraft` shapes behind them). `MinProtocol` moves because a client at 1 calling the removed `ideation-approve` misbehaves rather than merely lacking a feature; `MinDaemonProtocol` moves because the desktop now calls `idea-promote`, which a daemon at 1 does not serve. | 2 | 2 |
| 3 | 2026-09-03 | SC-4521 ideation retirement. Removed: the `ideation-start`, `ideation-reply` and `ideation-status` routes and the shapes behind them (`IdeationStartRequest`, `IdeationReplyRequest`, `IdeationStatus`, `IdeationMessage`, `IdeationMode`, `IdeationState`). No board surface had started a session since protocol 2; the engine was kept only as a restore path while background drafting proved itself. `MinProtocol` moves because a client at 2 declares and can call `ideation-status`, so it misbehaves rather than merely lacking a feature. `MinDaemonProtocol` stays at 2: the client gains no dependency on new daemon behaviour — `idea-create`, the one route that survives the removal, has served since protocol 1. | 3 | 2 |
| 4 | 2026-09-14 | SC-4923 user-initiated description recreation. Added: the `recreate-description` route and `RecreateDescriptionRequest`; `IdeaDraftRequest.Recreate`. `MinProtocol` stays at 3 — the change is purely additive, so a client at 3 lacks the feature but never misbehaves, and a protocol-4 daemon keeps serving it. `MinDaemonProtocol` moves to 4 because the desktop now calls `recreate-description`: on a daemon at 3 the route is unknown, the request falls through to the CLI, and the board banner shows a cobra "unknown command" instead of the rebuild-the-daemon error this gate exists to produce. | 3 | 4 |
| 5 | 2026-09-24 | SC-5330 deploy falls back to the one branch carrying the ticket's commits. Added: the hidden `--candidate-branches` flag the client appends to every forwarded `deploy` run in a git repo (`cmdforward.CandidateBranchesFlag`), carrying the branches the caller's own checkout found. `MinProtocol` stays at 3 — an old client simply never sends the flag, so a protocol-5 daemon keeps serving it. `MinDaemonProtocol` moves to 5 because the client now unconditionally appends the flag to every forwarded `deploy`: on a daemon at 4 the `deploy` command does not declare it, and cobra fails every `human deploy` with "unknown flag: --candidate-branches" instead of the one clear rebuild-the-daemon error this gate exists to produce. | 3 | 5 |
| 6 | 2026-09-24 | SC-5326 board badge names why a planning card resolved with nothing to plan. Added: `BoardCard.ResolvedReason` / `BoardViewCard.ResolvedReason` (`resolved_reason`/`resolvedReason`), carrying `merged`/`duplicate`/`escalated`/`rejected`. `MinProtocol` stays at 3 — the change is purely additive, so an old client keeps working and a protocol-6 daemon keeps serving it. `MinDaemonProtocol` stays at 5 — an older daemon simply omits `resolvedReason`, and the client's badge falls back to the plain "nothing to plan" rendering rather than misbehaving. | 3 | 5 |
| 7 | 2026-09-24 | SC-5369 container resource visibility. Added: the `container-stats` route (`internal/daemon/container_stats_route.go`), `ContainerResourceReport`/`ContainerEngineCapacity`, and the `--range` argument the client sends via `Client.QueryContainerResources`, reached from the new `human stats containers` command. `MinProtocol` stays at 3 — the change is purely additive, so a client at 3 lacks the command but never misbehaves, and a protocol-7 daemon keeps serving it. `MinDaemonProtocol` moves to 7, matching rows 2 and 4's precedent for a route the client calls unconditionally once the surface is invoked: on a daemon at 6, `container-stats` is unknown to `routeSimpleCommand`, the request falls through to the in-daemon CLI, and `human stats containers` dies with cobra's "unknown command \"container-stats\"" instead of the one clear rebuild-the-daemon error this gate exists to produce. | 3 | 7 |
| 8 | 2026-09-24 | SC-5516 ticket cost from the command line. Added: the `ticket-stats` route (`internal/daemon/ticket_stats_route.go`) serving the ledger's ranked `TopTicketSpend` scoped to the requesting project, with `--range`, `--limit` and an optional `--project` override, sent by `Client.QueryTicketSpend` from the new `human stats tickets` command; the single-key form of that command rides the existing `ticket-cost` route. `--project`, when sent, overrides the project the receiving connection would otherwise derive from its own cwd — needed because the CLI form's RunE re-enters the daemon over a fresh loopback connection when it is itself already running forwarded (a multi-project daemon), and that connection's cwd is the daemon process's own, not the original caller's (PR review, SC-5516). `MinProtocol` stays at 3 — additive, as row 7. `MinDaemonProtocol` moves to 8 for row 7's reason: the route is called unconditionally once the command is invoked, and on a daemon at 7 it would die with cobra's "unknown command \"ticket-stats\"" instead of the rebuild-the-daemon error. | 3 | 8 |
| 9 | 2026-09-25 | SC-3577 board-level flow signal. Added: `BoardView.Flow *BoardFlow` (`flow`, omitted when nil), the `BoardFlow` struct (`state`, `since`, `inFlight`, `keys`, `unreadable`) and the `BoardFlow*` state constants (`flowing`/`idle`/`stalled`/`unknown`). `MinProtocol` stays at 3 — purely additive; an old client simply never sees `flow` and renders as it does today. `MinDaemonProtocol` stays at 8 — an older daemon omits `flow` entirely, `flowNotice` on the client returns null, and the strip never renders; the client gains no dependency on new daemon behaviour, so nothing breaks, it just shows nothing. | 3 | 8 |
| 10 | 2026-09-25 | SC-3656 board card names the review→fix round. Added: `BoardCard.PRReviewRound`/`PRReviewRoundCap` (`pr_review_round`/`pr_review_round_cap`) and `BoardViewCard.PRReviewRound`/`PRReviewRoundCap` (`prReviewRound`/`prReviewRoundCap`), the loop round a running done-stage card is in and the `DefaultPRReviewRounds` bound it runs against. `MinProtocol` stays at 3 — purely additive, as row 6. `MinDaemonProtocol` stays at 8 — an older daemon simply omits both fields, and the badge falls back to today's "PR review…"/"fixing PR findings…" rendering with no round rather than misbehaving. | 3 | 8 |
