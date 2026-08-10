# The reaper — when a container/agent is killed

Every pipeline stage runs as one agent: a devcontainer holding a claude process,
a private git worktree, and an execution log directory. Something has to end
that run. This document lists **every condition under which the machine ends an
agent**, what it spares, what it costs the ticket, and what it leaves behind.

The container's PID 1 is `sleep infinity` — claude runs as an exec beside it, so
the container survives its agent by design and the rules below are what end it.
Docker's init runs in front of that `sleep` for the one thing a plain command
cannot do: reap the processes an exiting agent orphans, which otherwise stay
defunct for as long as the container lives (SC-4281).

It describes the system **as it is**. Its companion is
[`pipeline-fsm.json`](pipeline-fsm.json): that document follows one *ticket*
through its states; this one follows one *container*. They meet at the marker a
reap produces — a reap is how a stage transition happens when nobody posted a
marker on the way out.

Read this before changing the zombie sweep, the reconcile passes, the idle
budgets, or anything that stops an agent. Change the code and this document in
the same commit.

## The three signals

The machine never asks the agent whether it is alive. It has three signals, and
every rule below is built from them:

| Signal | Source | What it proves |
| --- | --- | --- |
| **Process liveness** | `pgrep -x claude` inside the container, every 5s | The process exists. A *hung* agent looks perfectly healthy here. |
| **Hook events** | The agent's Claude hooks POST to the daemon (`PreToolUse`, `PostToolUse`, `Notification`, `Stop`, `SessionEnd`) | The agent did something observable, and when. |
| **Outstanding model request** | The daemon's own proxy: a request sent and not yet answered (`internal/daemon/inflight.go`) | The agent is thinking — or, when the daemon cannot resolve the agent's connections, that it cannot tell. Three answers, not two (`internal/daemon/agentprogress.go`, `ModelRequestState`). |

The third exists because the first two together still misjudge a thinking agent:
extended reasoning emits no hook event and streams no output. Transcript mtime
and streamed-output heartbeats were both tried and disproven; the proxy's own
in-flight state replaced them (SC-3074).

The third signal is best-effort and the mapping it needs goes missing silently
— a daemon replaced under a running agent, an inspect returning no address, a
warm relaunch. Every one of those used to answer "no request open", the same
value a genuinely idle agent gives, so the reaper killed live work at the
three-minute line (SC-3853). "Unknown" is now a value the signal can carry, and
`RunAgentIPRepair` (`internal/daemon/agentiprepair.go`) rebuilds the mapping
every 30 seconds for any running agent that has none — logging once per agent
when it still cannot.

Progress is tracked per agent in its own map (`internal/daemon/agentprogress.go`),
not derived from the event ring — the ring evicts under load and is empty after a
restart, and losing progress that way kills live work.

## Idle budgets

`AgentProgress.Stalled` (`internal/daemon/agentprogress.go`) is the single
definition of "hung", and it is two numbers, not one:

| Condition | Budget | Constant |
| --- | --- | --- |
| No outstanding work at all | **3 minutes** of silence | `IdleGrace` |
| Inside a tool call **or** a model request in flight **or** the model-request state unknown | **30 minutes** of silence | `WorkingIdleGrace` |
| Waiting on a human (`Notification` — a permission prompt) | **never stalls** | `Blocked` |

Waiting on a local tool call and waiting on the model are the same thing from
the outside — outstanding work, from two sources — so either earns the generous
bound. Genuine idleness, with neither, gets the short one. A single fixed
timeout was wrong in both directions at once: it killed running test suites and
still made real hangs wait.

Unknown takes the generous bound deliberately. The machine must never kill live
work because it lost its own bookkeeping — the same rule reconcile already
states for an agent its progress probe does not know about.

## Every condition that ends an agent

### 1. Clean finish — the session ended

**Owner:** `RunAgentCleanup` (`internal/daemon/agentcleanup.go`), subscribed to
the hook event store.

A `Stop`, `SessionEnd`, or `StopFailure` event for an agent triggers a full
`Delete` (container stopped and removed, meta deleted) — but **only once claude
has actually exited**. The listener waits for that, polling every
`cleanupExitPoll` = 1s for up to `cleanupExitWait` = 5 minutes.

The wait is the whole point. An exit hook event carries the *container's* agent
name, so a subagent's ending is indistinguishable from its parent's — and a
parent that dispatched a subagent goes on working after it. Acting on the event
alone destroyed live runs: the container was removed one second later, the 10s
`ContainerStop` grace expired, and claude was SIGKILLed mid-tool-call, which the
board then read as "the run stopped before finishing this stage" (SC-3785).

So the event is a prompt to check, not a verdict:

- **claude gone** → tear down, as before.
- **claude still running when the budget runs out** → the event belonged to
  somebody else. Leave the run alone; its own ending will arrive later, and a run
  that instead dies silently is the zombie sweep's to catch.
- **the agent cannot be probed** → treated as ended. Unreachable is not evidence
  of a live run, and sparing it would strand containers on any docker hiccup.

Events are tracked by monotonic sequence, not by agent name: board stage agents
reuse the same deterministic name on every rebuild, and a name-keyed dedupe
leaked the re-run's container and worktree (SC-201).

This is the normal path. Everything below is the machine deciding for itself.

### 2. Zombie sweep — claude is gone

**Owner:** `RunAgentZombieSweep` (`internal/daemon/agentzombiesweep.go`), every
5 seconds.

An agent is reaped when its container is running, it is older than the 10-second
`zombieGracePeriod`, and `pgrep -x claude` reports nothing. This catches claude
failing to start, crashing without firing hooks, or a killed tmux pane.

**How the probe asks** (`devcontainer.ProcessRunning`). The exec attaches stdout
and stderr and drains them to EOF, then reads the exit code only once
`ExecInspect` reports the exec is no longer `Running`, polling up to
`execSettleTimeout` = 5s for that. Both halves are load-bearing: the stream is
what says the process ended, so it only synchronises if the output is attached,
and a still-running exec carries `ExitCode` 0 — indistinguishable from "pgrep
found claude". An exec created without attachment therefore reported *every*
container's claude as alive, and no agent was ever reaped by this rule; the
containers outlived their agents for hours while the board rendered them live
(SC-4281). A probe that never settles is an error, not an absence, and escalates
through § 3 rather than answering.

**Spared:** an agent started without a prompt (bare `human agent start NAME`)
never launches claude at all. It is reaped only once claude has been *observed*
running for it at least once (`seenClaude`) — otherwise a deliberately idle
agent dies within seconds of coming up (SC-236). That spare is absolute; it
survives even the escalation below.

### 3. Zombie sweep — the container is unreachable

**Owner:** same loop.

A liveness check can fail transiently (the container removed between list and
check), which is tolerated by skipping the tick. But a post-suspend Docker/exec
disruption fails *persistently*, every tick, and left unbounded it skips the reap
forever while the board card spins at "reviewing…" (SC-263). After
`zombieMaxProcessCheckFailures` = **3 consecutive failures** (~15s at the 5s
interval) the agent is presumed unreachable-and-dead and reaped. A single
successful check clears the streak.

### 4. Zombie sweep — silence reap (board agents only)

**Owner:** same loop, via `hungBoardAgent`.

claude is *still running* but the agent has been silent past its idle budget
(§ Idle budgets). Process liveness alone reports such an agent as healthy
forever, so this is the only rule that can ever catch a hang (SC-1600).

It applies **only to board stage agents** — names of the form
`board-<KEY>-<stage>`. An interactive agent is deliberately excluded: a human
thinking between turns looks identical to a hang, and reaping it discards live
work.

It also requires an answer, not the absence of one. An agent whose
model-request state is unknown gets `WorkingIdleGrace` (30 minutes) exactly
like one with outstanding work — it is reaped later, not never, if it stays
silent that long. The reap log carries `model_request` so a reap that did
happen names which answer it acted on.

The reap carries its reason out as a sentinel: the synthesized `StopFailure`
event's `ErrorType` is `reaped-silent:<idle>` (`ReapSilenceErrorType`), which
routes the exit to the **uncharged** relaunch instead of the charged failure path
(SC-2447). See § What a reap costs.

A silence reap onto a stage that is **already** stopped, with no relaunch since,
posts nothing, relaunches nothing, and spends no budget (SC-3857): the exit
dispatcher's `stageAlreadyFailed` check reads the stage's own newest marker
before any of §§ 4–6 can post their own, so a second, unrelated exit event for a
run already declared dead is absorbed rather than re-told. The one case this does
NOT absorb is a genuine reap **after a relaunch that actually started an agent** —
such a relaunch posts the stage's own `*-started` marker before the agent that
follows it can exit, which flips the guard back off, so a repeated hang past
`MaxSilenceReaps` still escalates exactly as below. A relaunch the launcher
**refused** because an agent for `(key, stage)` is already running on this machine
posts no marker at all (SC-4244): the guard stays on, deliberately, because the
run it would re-tell about is the one still going — and, for the same reason, that
refusal does not re-date `StageEnteredAt`, so § 5's grace is measured from the
start of the run that is actually running rather than from the last attempt to
replace it.

The same `stageAlreadyFailed` check also sits ahead of a genuine death (§ 2,
§ 3, § 6), but there it is narrower: it suppresses only the repeat POST of an
identical `*-failed` marker, never the automatic in-place relaunch that
follows. The ordinary, prompt-instructed ending for implementation and review
IS the skill posting its own `*-failed` marker and recording its own
`stage.<stage>` outcome before it exits (`human-review-skill.md`,
`human-autofix-skill.md`, `human-security-fix-skill.md`,
`human-pickup-review-skill.md`) — so by the time the daemon's exit watcher
processes that same ending, the marker it posted is already the stage's
newest, indistinguishable by marker history alone from a stale duplicate. The
relaunch decision is read from the stage's own recorded exit class
(`deps.Retry.Outcome`) instead, a signal independent of the marker thread, so
a genuine retryable ending still gets its bounded `tryRelaunch` even though its
own marker cannot be re-posted. (An earlier cut of this guard returned before
`tryRelaunch` ran at all and silently dropped that relaunch for every one of
these endings — not a rare unrecorded-exit corner, the everyday
retryable-review path; a second-opinion review caught it before it shipped.)
Silence reap (this section) and the needs-person wall are the exception: each
decides its post and its relaunch (or lack of one) in a single synchronous
step the daemon itself performs — never a skill — so an already-failed marker
there can only be a fully-completed EARLIER cycle, and both the post and the
relaunch stay suppressed for those two, exactly as stated above.

### 5. Reconcile — stuck-running card, agent alive but stalled

**Owner:** `reconcileStuckRunning` / `hungLiveAgent`
(`internal/daemon/board_reconcile.go`), every `BoardReconcileInterval` = 2
minutes ± 50% jitter.

The durable twin of the silence reap, working from the *card* rather than the
container. All of these must hold:

- the card derives to a running state, with no active PR review→fix loop;
- it carries no open `[human:options]` block for its own stage or an earlier one
  (that is a deliberate human pause, not a hang);
- its stage has a `*-failed` marker header available;
- it has sat past its grace — `StuckRunningGrace` = **15 minutes** for every stage except a card recorded as *deploying*, whose newest done-stage marker is `[human:deploy-started]`: that one is left alone for `deployTimeout + StuckRunningGrace` = **60 minutes**, because a deploy has no agent to probe and its CI gate legitimately blocks for 45 (`stuckPastGrace`, `internal/daemon/board_reconcile.go`), and measured from the ENGINE's clock rather than the marker post: `DeployBranch` records the instant it leaves the unbounded `deployGate`, and while a run is still queued the clock is the last time the queue moved — so a deploy waiting behind another is spared while the queue advances and judged once it has stopped (SC-4150). The marker clock is the fallback, used where this machine is not the one running the deploy (a peer daemon, a restart that lost the record), so the pass stays bounded rather than exempt. The rule is keyed by the in-flight run, not by which marker is newest, so the approve-branch (`[human:pr-review-passed]`) and deploy-fixer (`[human:deploy-fix-started]`) routes get the same clock as `[human:deploy-started]`. Where the clock is the marker, it is the stage's newest *classified* marker — and a relaunch refused because an agent is already running posts none, so refused relaunches no longer refresh it and the grace runs from the start of the run that is actually running (SC-4244);
- it passed the `forTakeover` ownership gate — this machine participates in the
  project, no peer daemon owns the stage, and the branch resolves here (SC-2047);
- and the stage agent, which *is* alive on this machine, reports stalled.

Then the agent is stopped (a full `DeleteAgent` under a 60s timeout) *before*
anything relaunches — otherwise two agents work the same stage — and the card is
reddened with the silence-reap wording.

A stop that fails, or a `stopAgent` that is unwired, leaves the card alone. So
does an agent the progress probe does not know about: killing live work on absent
evidence is the one failure this must never risk.

### 6. Reconcile — stuck-running card, agent vanished

Same pass, same preconditions, but no agent for `(key, stage)` is alive at all.
Nothing is reaped here — the container is already gone. This is the fallback for
a death the live failure watcher missed (a daemon restart, a dropped event), and
unlike § 5 it is a genuine unexplained death, so it reds the card and relaunches
on the **charged** path.

The deploy grace above applies here too — a `human deploy` on its CI gate has
*no* agent by construction, so a vanished agent is not evidence a deploy is dead
until its own timeout has passed.

### 7. Reconcile — orphaned on a closed ticket

**Owner:** `reconcileOrphanedAgents` (`internal/daemon/board_reconcile_orphan.go`).

A board agent whose PM key matches no open card, and whose ticket a
`ClosedTicketProbe` **confirms** is done/closed, is stopped. Such a run would
otherwise keep working invisibly against a closed ticket — holding its container
and worktree, posting markers, even pushing commits for work the user called off.

It works from the agent side, so a healthy board costs nothing: an agent matching
an open card is dismissed without a tracker call. Every uncertainty resolves to
*leave it running* — an unparseable agent name, a probe error, or a ticket merely
absent from the open list rather than confirmed closed. Absence is not proof; a
flaky per-ticket fetch looks the same.

**What it leaves behind.** Each stop posts a `[human:run-cancelled]` marker
naming the stage and the agent. Closing fires from outside the marker bus, so a
card used to go from running to gone with nothing on the thread saying work had
been interrupted: of every closed PM ticket on this board (382, measured
2026-08-08), 60 were closed out of a non-terminal state and 4 out of a running
one. The record is posted **after** the stop and is best-effort — the stop is the
property this pass exists for, and a tracker that will not take a comment, which
a just-closed ticket may well refuse, must never leave an agent running against
called-off work.

### 8. Close is cancellation

**Owner:** `StopAgentsForPMKey` (`internal/daemon/close_cancel.go`), called from
the close gate before the ticket transitions.

Closing a ticket from the board reads to the user as "stop this", so it stops
every live board agent claiming that key, across all stages, within a 90-second
budget. The close is **gated** on it: if the stop cannot be confirmed for every
agent — or the liveness probe itself failed — the ticket stays open, and thus
stays reachable by the reconcile net, rather than closing over a run that refused
to die.

### 9. Pre-launch teardown of a same-named agent

`Manager.Start` refuses to start over a still-running agent, so the launchers for
the singleton scan agents (`features`, `findbugs`, `findsecurity`, `mockups-<slug>`)
delete any prior agent of that name first. This makes a retry after a stale or
crashed run idempotent.

### 10. A person asks

`human agent stop NAME` runs the same `Manager.Stop` choke point as everything
above.

## What is never reaped

Collected in one place, because the spares are the load-bearing part:

- **A run whose claude is still running when an exit event names it.** The event
  was a subagent's; the run keeps working (SC-3785).
- **An agent blocked on a permission prompt.** It is waiting for a person; a
  relaunch discards the question instead of answering it.
- **An interactive (non-board) agent that is silent.** Only a board stage agent's
  silence is unambiguous.
- **An idle-by-design agent** (started with no prompt) that has never been seen
  running claude.
- **An agent the progress probe does not know about** — a restarted daemon, or an
  agent that has not emitted its first event. Unknown is never read as hung.
- **An agent whose model-request state is unknown is never reaped on the SHORT
  budget** — the daemon could not resolve its connections to a name, so its
  in-flight count means nothing. Absent evidence still gets `WorkingIdleGrace`
  (30 minutes), not `IdleGrace` (3); it is reaped later on continued silence,
  never on the short budget (SC-3853).
- **A card with an open `[human:options]` block** for its own or an earlier stage.
- **A card in an active PR review→fix loop**, whose half-agents legitimately come
  and go between rounds.
- **An outage card** (`BoardOutage`): it is waiting on the substrate, not hung. It
  is relaunched uncharged each tick until `OutageWaitBound` = **6 hours**, after
  which it is handed to a person — still uncharged.
- **Anything on a machine that does not own the stage** (`forTakeover` gate).
- **Everything, when the liveness list or the closed-ticket probe errors.** A probe
  blip is not evidence.

## What a reap costs the ticket

| Ending | Charged against `DefaultStageRetries` (=2)? | Bound |
| --- | --- | --- |
| Silence reap (§ 4, § 5) | **No** — `relaunchSilenceReap`. The work did not fail; a judgement about the work did (SC-2447). | `MaxSilenceReaps` = 3 relaunches; the 4th posts a give-up marker naming the count and stops. A reap onto an already-stopped stage with no relaunch since costs nothing at all — it is absorbed before any of this runs (SC-3857). |
| Genuine death — claude gone, container unreachable, agent vanished (§ 2, § 3, § 6) | **Yes** — `tryRelaunch`. | 2 automatic relaunches per stage, then the card reds for a person. |
| Outage (substrate unreachable) | **No** — `relaunchOutage`. | `OutageWaitBound` = 6h, then handed to a person. |
| Needs-person walls (revoked credential, exhausted billing) | **No**, and never auto-relaunched — the next attempt hits the same wall. | — |

Repetition is what gets escalated, not any single stop. A silence reap costs
nothing precisely so that a *repeated* silence reap is legible as its own
problem, and `MaxSilenceReaps` is what makes that repetition visible instead of
hidden. Both give-up markers dedup on a pinned sentinel string so two daemons
reaching the cap at once do not both post.

## What a reap leaves behind

The teardown choke point is `Manager.stopLocked` (`internal/agent/manager.go`):

1. **Transcript and outcome are persisted first**, before the container (and its
   in-container `~/.claude/projects` transcript) is destroyed — `PreserveExecutionArtifacts`.
2. **The container is stopped and removed**, and its devcontainer meta deleted.
3. **The worktree survives unless the run succeeded.** The gate is positive
   success, not "did not fail": only a run that posted its handoff has its
   private worktree removed. A reaped run — and a clean exit that never handed
   off — *keeps* the worktree beside its execution log for forensics and resume,
   because a no-handoff exit is precisely the case where uncommitted work exists
   to lose (SC-731). The kept worktree has its HEAD **detached**, so it stops
   owning `refs/heads/<branch>` and cannot freeze the shared repo's local branch
   (SC-2322).
4. **`outcome.json` records the classification** `DiagnoseFailure` keys off, so
   the failed marker says what actually broke instead of a generic stage line.
5. **`output.log` always ends with an exit trailer** (`[human] claude exec exited
   with code …`), written by the tee when the exec stream EOFs — so an
   in-container run that dies while its warm container stays up still leaves a
   diagnosable log rather than a 0-byte void (SC-1688). The tee never overwrites
   an existing `outcome.json`, so it cannot clobber a `reaped` classification.
6. **Execution directories are pruned after 90 days** (`execRetentionDays`,
   `PruneExecutions`).
7. **A late-arriving result is reconciled, not left contradicting the reap.**
   `RunLateResultReconcile` (`internal/daemon/board_latereconcile.go`) scans
   open cards for a stage marked failed followed by that same stage's success
   with no relaunch in between, and records it with a
   `[human:late-result-reconciled]` marker — so a ticket whose reap turned out
   wrong carries an explanation instead of a failure and a success that
   silently disagree (SC-3853).

One asymmetry worth knowing when reading artifacts: the zombie sweep marks the
meta `StatusFailed` before teardown, so its runs record `reason: "reaped"`. The
reconcile pass's hung-agent stop goes through `dockerAgentCleaner.DeleteAgent`,
which does not, so a stage stopped by § 5 records `reason: "completed"` even
though the machine killed it. The card's marker still says silence reap; the
run's `outcome.json` does not.

## The daemon's own exit is not a reap, and ends work anyway

Everything above is the machine ending an agent on purpose. The other way work
ends is the daemon process going away underneath it — and the work that dies
there is not in a container at all. A forwarded command executes **inside** the
daemon (`Server.executeCommand`), and the long one is `human deploy`, which can
sit up to `deployTimeout` = **45 minutes** on its CI gate. It leaves no
container and no execution log; since SC-3852 it does leave a marker — the
entry point (`internal/daemon.StartDeploy`) posts `[human:deploy-started]`
before anything is pushed — so an interrupted deploy is now visible on the
ticket as a done-stage card that started and never finished, rather than being
indistinguishable from one that never started.

Three things keep that from happening silently:

- **A rebuild postpones.** The self-restart watcher hands over only when
  `BlockingOps()` is zero, and a forwarded command counts itself, so a handover
  never commits while a deploy is on its gate. (`handoverCoordinator.watch`,
  `cmd/cmddaemon/handover_unix.go`.)
- **A stop names what it is waiting for.** `human daemon stop` reads the
  in-flight count *before* signalling — a shutting-down daemon has closed the
  listener the question travels over — and past a **5s** grace it either reports
  a daemon stuck with nothing in flight, or says how many operations it is
  finishing and waits `--wait` (default `stopDrainDefault` = **30s**; the desktop
  close flow shells out to this command, so the default stays short). Outlasting
  the wait is not a failure: the daemon exits on its own when the work is done.
  `--force` ends it now, abandoning the work.
- **The gate says what it is doing.** `DeployBranch` logs queued → started → PR
  open → CI verdict → merged → done, with a "CI still running" heartbeat every
  `deployWaitHeartbeat` = **10 polls** (~5 minutes). A deploy that stops
  mid-trail is what an interruption looks like in the log.

What none of this can cover is `SIGKILL`: it cannot be caught, the deploy dies
with the process, and nothing is recorded. This is why `--force` signals one pid
and why killing by name is the wrong reach — every `human` process on the
machine answers to that name, including the CLI half of the deploy someone is
waiting on.

## Timings and constants, in one place

| Constant | Value | Where |
| --- | --- | --- |
| `cleanupExitPoll` | 1s | `internal/daemon/agentcleanup.go` |
| `cleanupExitWait` | 5m | same |
| `cleanupProbeTimeout` | 5s | same |
| `zombieSweepInterval` | 5s | `internal/daemon/agentzombiesweep.go` |
| `zombieGracePeriod` | 10s | same |
| `zombieMaxProcessCheckFailures` | 3 (~15s) | same |
| `zombieReapHardDeadline` | 45s | same |
| `execSettleTimeout` | 5s | `internal/devcontainer/exec_probe.go` |
| delete timeout inside a reap | 30s | same |
| `IdleGrace` | 3m | `internal/daemon/agentprogress.go` |
| `WorkingIdleGrace` | 30m | same |
| `agentIPRepairInterval` | 30s | `internal/daemon/agentiprepair.go` |
| `BoardReconcileInterval` | 2m ± 50% jitter | `internal/daemon/board_reconcile.go` |
| `StuckRunningGrace` | 15m | same |
| deploy-card grace (`stuckPastGrace`, not a named constant) | 60m (`deployTimeout` + `StuckRunningGrace`), from the engine's dequeue where known, else the marker | `internal/daemon/board_reconcile.go` |
| hung-agent stop timeout | 60s | `cmd/cmddaemon/daemon.go` |
| close-cancel stop budget | 90s | same |
| `MaxSilenceReaps` | 3 | `internal/daemon/board_failure.go` |
| `DefaultStageRetries` | 2 | `internal/daemon/board_retry.go` |
| `OutageWaitBound` | 6h | `internal/daemon/board_outage.go` |
| `execRetentionDays` | 90 | `internal/agent/agentlog.go` |
| `deployTimeout` | 45m | `internal/daemon/board_transition.go` |
| `deployWaitHeartbeat` | 10 polls (~5m) | same |
| `LateResultReconcileInterval` | 5m | `internal/daemon/board_latereconcile.go` |
| `stopGrace` | 5s | `cmd/cmddaemon/daemon.go` |
| `stopDrainDefault` | 30s (`--wait`) | same |

## Why a single reap can never stall the sweep

One sweep goroutine reaps every agent, so a stalled `CopyTranscript` inside
`DeleteAgent` would otherwise stop every later agent from ever being reaped
(SC-427). Past `zombieReapHardDeadline` = 45s the reap is abandoned to the
background — the goroutine keeps its own 30s delete budget and finishes into a
buffered channel — and the loop advances. The agent's cross-tick memory is
deliberately left in place so the next tick retries it.

The liveness check has the same property: its exec stream is drained to EOF with
a watchdog that closes the attachment on context cancellation, so a stalled
stream cannot park the loop either.

## A reap is a transition

A reaped agent by definition died without emitting the exit hook the board
watcher listens for. The daemon therefore **synthesizes** a `StopFailure` event
for it (`cmd/cmddaemon/daemon.go`), so the reap path and the hook-driven exit
paths converge on one marker-posting code path (SC-206) — otherwise the board
card spins forever.

That synthesized event is why reaping belongs in the state machine and not
beside it: the marker it produces is a transition in
[`pipeline-fsm.json`](pipeline-fsm.json) exactly like one an agent posts for
itself. A change to a reap condition that changes which marker lands is a change
to the machine, and both documents have to move together.
