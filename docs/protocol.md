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

## Ledger

| Protocol | Date | Change | MinProtocol | MinDaemonProtocol |
|---|---|---|---|---|
| 1 | 2026-07-21 | Protocol handshake introduced (integer gate on both sides). Pre-protocol clients remain gated by the legacy `MinClientVersion` ≥ 0.21.0 check (last legacy break: the HUM-160 permission-grant cycle). | 1 | 1 |
| 2 | 2026-08-26 | SC-4608 background idea drafting. Added: the `idea-promote` route; `DescEditStartRequest.Promoted`; `BoardCard.TBACount` / `BoardViewCard.TBACount`. Removed: the `ideation-approve` route, `IdeationApproveRequest`, `IdeationStartRequest.EvolveKey`/`EvolveLabels`, `IdeationStatus.Question`/`Draft` (and the `IdeationQuestion`/`IdeationDraft` shapes behind them). `MinProtocol` moves because a client at 1 calling the removed `ideation-approve` misbehaves rather than merely lacking a feature; `MinDaemonProtocol` moves because the desktop now calls `idea-promote`, which a daemon at 1 does not serve. | 2 | 2 |
| 3 | 2026-09-03 | SC-4521 ideation retirement. Removed: the `ideation-start`, `ideation-reply` and `ideation-status` routes and the shapes behind them (`IdeationStartRequest`, `IdeationReplyRequest`, `IdeationStatus`, `IdeationMessage`, `IdeationMode`, `IdeationState`). No board surface had started a session since protocol 2; the engine was kept only as a restore path while background drafting proved itself. `MinProtocol` moves because a client at 2 declares and can call `ideation-status`, so it misbehaves rather than merely lacking a feature. `MinDaemonProtocol` stays at 2: the client gains no dependency on new daemon behaviour — `idea-create`, the one route that survives the removal, has served since protocol 1. | 3 | 2 |
