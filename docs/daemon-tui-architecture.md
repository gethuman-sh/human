# Daemon-TUI Architecture

## Overview

The TUI (`human tui`) is a Bubble Tea terminal application that displays Claude agent instances, tracker status, issues, tool statistics, and network activity. The daemon (`human daemon`) is a long-running TCP server that holds tracker credentials, processes hook events from Claude instances, and manages agent containers.

## Communication Model

### Current: Polling

```
TUI ─── 100ms ──→ daemon  FetchQuick (hooks, network events)
TUI ─── 2s    ──→ daemon  FetchFull  (all RPCs + pgrep/docker discovery)
TUI ─── demand ─→ daemon  issues, log-mode, confirms
```

The TUI runs two polling loops:
- **Fast tick (100ms)**: `FetchQuick` carries instances forward from the previous snapshot and only updates hook state and network events from the daemon. New/removed instances are invisible until the next full tick.
- **Full tick (2s)**: `FetchFull` runs complete instance discovery (pgrep + docker) and all daemon RPCs in parallel.

Problems:
- 100ms polling burns CPU for marginal benefit
- New/removed instances take up to 2s to appear/disappear
- `FetchQuick` carry-forward creates stale state
- Three fetch modes (Skeleton, Quick, Full) add complexity

### Target: Event-Driven with Polling Fallback

```
TUI ←── subscribe ──── daemon  push: hook changes, agent lifecycle
TUI ─── 2s heartbeat ─→ daemon  FetchFull (fallback, periodic refresh)
TUI ─── demand ────────→ daemon  issues, log-mode, confirms
```

The daemon pushes a lightweight "something changed" signal over a persistent TCP connection. The TUI reacts by running `FetchFull`. The 2s full tick remains as a heartbeat/fallback for missed events or daemon restarts.

Simplifications:
- Remove 100ms fast tick entirely
- Remove `FetchQuick` and its carry-forward logic
- Two fetch modes instead of three (Skeleton for first render, Full for everything else)
- One tick interval instead of two

## Daemon Architecture

### Server

TCP server at `localhost:19285`. Each connection receives one JSON request line, the server routes it, and sends one or more JSON response lines.

```
cli/internal/daemon/server.go    Server, handleConn, routeIntercept, executeCommand
cli/internal/daemon/protocol.go  Request, Response, SubscribeEvent
cli/internal/daemon/client.go    RunRemoteCapture, GetHookSnapshot, Subscribe, ...
```

### Intercepted Routes (in-memory, fast)

| Route | Handler | Data Source |
|-------|---------|-------------|
| `hook-event` | Append to HookEventStore | Claude hook input |
| `hook-snapshot` | HookEventStore.Snapshot() | In-memory ring buffer |
| `network-events` | NetworkEventStore.Snapshot() | In-memory buffer |
| `tool-stats` | StatsStore.BuildToolStats() | SQLite |
| `tracker-diagnose` | LoadAllInstancesWithResolver | Config + vault |
| `tracker-issues` | Provider.ListIssues (parallel) | Tracker APIs |
| `pending-confirms` | PendingConfirmStore.Snapshot() | In-memory |
| `confirm-op` | PendingConfirmStore.Resolve() | In-memory |
| `agent-stop-async` | DecommissionAgent + background StopContainer | Agent metadata + Docker |
| `subscribe` | Long-lived connection, streams events | HookEventStore subscription |
| `log-mode` | Get/set proxy log mode | In-memory |

### Routed Commands (cobra execution)

All other requests go through `executeCommand`: a fresh cobra.Command tree is created via `CmdFactory()`, args are set, and `cmd.Execute()` runs in-process. The daemon's vault resolver is injected via context so tracker credentials are resolved without repeated 1Password calls.

### Event Stores

**HookEventStore** (`cli/internal/daemon/hookstore.go`): Thread-safe ring buffer of Claude hook events. Supports `Subscribe()` which returns a notification channel (buffered, coalescing). Used by:
- `RunAgentCleanup` (reacts to Stop/SessionEnd events)
- `subscribe` endpoint (pushes to TUI)

**NetworkEventStore** (`cli/internal/daemon/networkstore.go`): Deduplicated buffer of ambient network activity from the HTTPS proxy.

**StatsStore** (`cli/internal/stats/`): SQLite persistence for tool call statistics with hourly aggregation.

### Agent Lifecycle

```
human agent start <name>
  └→ devcontainer create + docker run
  └→ writes ~/.human/agents/<name>.json (metadata)
  └→ Claude starts inside container

Claude exits (or tmux pane killed)
  └→ tmux shell chain: human agent stop --async <name>
  └→ daemon: DecommissionAgent (deletes metadata immediately)
  └→ daemon: emits AgentStopped hook event (notifies subscribers)
  └→ daemon: StopContainer in background goroutine (2s timeout)

Fallback: zombie sweep (5s interval, 10s grace)
  └→ checks running containers for Claude process
  └→ cleans orphaned containers where Claude crashed without hook
```

## TUI Architecture

### Bubble Tea Model

```
cli/cmd/cmdtui/tui.go    model, Init, Update, View
```

The model holds:
- `snap *monitor.Snapshot` -- current display state
- `fetching bool` -- guards concurrent fetches
- `fetchGen uint64` -- monotonic counter to discard stale results
- `showSplash bool` -- initial logo display period
- `daemonEvents <-chan SubscribeEvent` -- subscription channel

### Fetch Cycle

1. **Init**: `fetchSkeleton` (instant, local reads) + 2s splash timer + subscribe to daemon
2. **Skeleton arrives**: Sets `snap`, renders dashboard with "Connecting trackers..." / "Discovering instances..." spinners. Chains `fetchHeavy`.
3. **Heavy arrives**: Full dashboard. Clears splash.
4. **Daemon event**: Triggers immediate `FetchFull`.
5. **2s heartbeat**: Triggers `FetchFull` as fallback.

### Monitor

```
cli/internal/claude/monitor/monitor.go    Monitor, FetchSkeleton, FetchHeavy, FetchFull
cli/internal/claude/monitor/snapshot.go   Snapshot, DaemonState, InstanceView
```

`FetchHeavy` runs daemon RPCs (tracker diagnose, hooks, network events, tool stats) in parallel with instance discovery (pgrep + docker). After discovery completes, it builds session state from JSONL parsing and hook overlays.

### Instance Discovery

```
cli/internal/claude/discovery.go    CombinedFinder, HostFinder, DockerFinder
```

- **HostFinder**: `pgrep -a claude` on host, resolves JSONL from session files
- **DockerFinder**: `docker ps` + single `sh -c` probe per container (pgrep + printenv + readlink consolidated into one exec call)
- **CombinedFinder**: Runs both finders in parallel

### Session Parsing

```
cli/internal/claude/logparser/parser.go    FileParser, processLine, SessionState
```

Incremental JSONL parser that tracks session identity, status, tasks, subagents, and usage. Resets accumulated state when a new session starts (different `sessionId` in the same file).

## Data Flow Diagram

```
Claude instances
  │ hook events (hook-event RPC)
  ▼
┌─────────────────────────────────────────────┐
│ Daemon                                      │
│                                             │
│  HookEventStore ──subscribe──→ TUI (push)   │
│       │                                     │
│       ├──→ RunAgentCleanup (auto-stop)      │
│       └──→ Snapshot() (on TUI poll)         │
│                                             │
│  NetworkEventStore ──→ Snapshot()            │
│  StatsStore (SQLite) ──→ BuildToolStats()   │
│  TrackerInstances ──→ DiagnoseTrackers()    │
│                      ──→ ListIssues()       │
│                                             │
│  AgentCleaner                               │
│    ├── DecommissionAgent (metadata)         │
│    ├── StopContainer (docker, 2s timeout)   │
│    └── ZombieSweep (5s interval, 10s grace) │
└─────────────────────────────────────────────┘
  │ subscribe events + poll responses
  ▼
┌─────────────────────────────────────────────┐
│ TUI                                         │
│                                             │
│  Monitor                                    │
│    ├── FetchSkeleton (local reads, instant) │
│    ├── FetchHeavy (parallel RPCs + discovery)│
│    └── FetchFull = Skeleton + Heavy         │
│                                             │
│  Discovery (parallel)                       │
│    ├── HostFinder (pgrep)                   │
│    └── DockerFinder (docker ps, parallel)   │
│                                             │
│  View: header → status → tabs → instances   │
│        → trackers → issues → stats → net    │
└─────────────────────────────────────────────┘
```

## Daemon wire contract: the public `human-daemon-client` module

The daemon's TCP+JSON wire surface (the client functions and the shared protocol
types) lives in a standalone public module,
[`human-daemon-client`](https://github.com/gethuman-sh/human-daemon-client),
imported as `client`. The core repo imports it and **aliases** its own daemon
identifiers to the contract types (`type TrackerIssuesResult =
client.TrackerIssuesResult`, etc.), so the daemon serializes literally the same
structs the GUI and TUI deserialize — one source of truth for the wire format.
The contract has zero dependency on `internal/tracker`/`internal/forge`: it
defines its own `Issue`/`Category` wire DTOs, and the daemon maps
`[]tracker.Issue → []client.Issue` at the fetch seam (`cmd/cmddaemon/daemon.go`,
`toContractIssues`) while the in-repo TUI maps the reverse at the
daemon→TUI boundary (`cmd/cmdtui/tui.go`, `toTrackerIssues`).

### TUI sufficiency (the contract covers every daemon symbol the TUI uses)

The in-repo TUI today imports `internal/daemon`, but every type/func it uses
aliases to a `client` identifier, so the contract is already sufficient for the
eventual TUI extraction — no contract change will be needed. Because core aliases
each symbol, the in-repo TUI continuing to compile under `make check` **is** the
compile-check that the contract is complete: a missing symbol would fail to
alias.

| TUI symbol (`daemon.X`) | Contract origin (`client.X`) |
| -- | -- |
| `ReadInfo` (returns `DaemonInfo`), `ProjectInfo` | `ReadInfo` / `DaemonInfo` / `ProjectInfo` |
| `GetTrackerIssues`, `TrackerIssuesResult` | `GetTrackerIssues` / `TrackerIssuesResult` |
| `Subscribe`, `SubscribeEvent` | `Subscribe` / `SubscribeEvent` |
| `GetPendingConfirms`, `PendingConfirm`, `SendConfirmDecision` | same names |
| `GetLogMode`, `SetLogMode`, `RunRemoteCapture` | same names |
| `NetworkEvent` (type, rendered) | `NetworkEvent` |
| `ReadAlivePid` | `ReadAlivePid` (independent read-from-disk copy in the contract) |

`ReadAlivePid`/`PidPath`/`IsProcessAlive` are the one exception: the contract
carries its own independent copies (read-from-disk, no shared in-memory state)
rather than aliasing core's, so they are validated by the contract module's own
build/tests, not by the core alias compile-check.
