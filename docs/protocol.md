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
