package daemon

import (
	"context"
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/rs/zerolog"
)

// AgentCleaner stops and removes an agent by name.
type AgentCleaner interface {
	DeleteAgent(ctx context.Context, name string) error
	// DecommissionAgent removes the agent from the list immediately and
	// returns the container ID for background teardown. This makes
	// "human agent list" responsive while the slow container stop happens
	// asynchronously.
	DecommissionAgent(name string) (containerID string, err error)
	// StopContainer stops and removes a container by ID.
	StopContainer(ctx context.Context, containerID string) error
}

// AgentProcessAlive reports whether the named agent's claude process is still
// running. It is the evidence the cleanup listener needs because an exit hook
// event does NOT prove the run ended: the events carry the container's agent
// name, so a subagent's ending is indistinguishable from its parent's, and a
// parent that dispatched a subagent goes on working after it (SC-3785). An
// error means the agent could not be reached, which is treated as ended — the
// same teardown that happened unconditionally before this signal existed.
type AgentProcessAlive func(ctx context.Context, agentName string) (bool, error)

var (
	// cleanupExitPoll is how often the teardown re-asks whether claude has
	// exited. Short: a normal ending must not feel slower than the previous
	// fixed one-second wait.
	cleanupExitPoll = 1 * time.Second
	// cleanupExitWait bounds that wait. Past it the exit event is abandoned
	// rather than acted on: the run is still going, so it will announce its
	// real ending later, and a run that instead dies silently is the zombie
	// sweep's job. Bounding it keeps a long-lived run from holding a goroutine
	// for its whole life.
	cleanupExitWait = 5 * time.Minute
	// cleanupProbeTimeout bounds one liveness probe so a stalled docker exec
	// cannot park the wait for the whole budget.
	cleanupProbeTimeout = 5 * time.Second
)

// RunAgentCleanup watches for exit hook events from devcontainer agents and
// stops the container and removes the worktree once the run has actually ended.
//
// alive is what makes "actually" load-bearing. A Stop event was previously
// taken as proof the session was over, and the container was destroyed one
// second later — which SIGKILLed runs that were still working, because a
// subagent's Stop arrives under the parent's agent name (SC-3785). A nil probe
// restores that unconditional behaviour (the package's "nil disables"
// convention), so existing callers keep compiling and behaving as before.
func RunAgentCleanup(ctx context.Context, store *HookEventStore, cleaner AgentCleaner, alive AgentProcessAlive, logger zerolog.Logger) {
	if store == nil || cleaner == nil {
		return
	}

	ch := store.Subscribe()
	defer store.Unsubscribe(ch)

	logger.Info().Msg("agent cleanup listener started")

	// Track events by monotonic sequence, not by agent name: board stage agents
	// reuse the same deterministic name on every rebuild, so a name-keyed
	// lifetime dedupe leaked the re-run's container and worktree (SC-201).
	var lastSeq uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			newEvents, seq := store.EventsSince(lastSeq)
			lastSeq = seq
			for _, evt := range newEvents {
				if evt.AgentName == "" {
					continue
				}
				if !hookevents.IsRunEnd(evt.EventName) {
					continue
				}
				go cleanupAfterExit(ctx, cleaner, alive, evt.AgentName, logger)
			}
		}
	}
}

// cleanupAfterExit tears one agent down once its claude process is gone. Split
// out of the watch loop so the wait's branching costs its own function rather
// than the loop's complexity budget.
func cleanupAfterExit(ctx context.Context, cleaner AgentCleaner, alive AgentProcessAlive, name string, logger zerolog.Logger) {
	if !waitForAgentExit(ctx, alive, name, logger) {
		return
	}
	logger.Info().Str("agent", name).Msg("auto-cleaning agent after session end")
	deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := cleaner.DeleteAgent(deleteCtx, name); err != nil {
		logger.Warn().Err(err).Str("agent", name).Msg("agent cleanup failed")
	}
}

// waitForAgentExit blocks until the agent's claude process is gone, and reports
// whether the caller may tear the agent down.
//
// It returns false only for the case this exists to prevent: claude is provably
// STILL RUNNING when the wait runs out, meaning the exit event did not belong to
// this run. Everything else — no probe wired, a probe that cannot reach the
// agent, a cancelled daemon — resolves to teardown or to leaving the loop, never
// to killing a run that is demonstrably alive.
func waitForAgentExit(ctx context.Context, alive AgentProcessAlive, name string, logger zerolog.Logger) bool {
	// The original fixed pause, kept as the first step: claude fires its exit
	// hook from inside its own process, so it is still running at this instant
	// even on a perfectly normal ending.
	select {
	case <-time.After(cleanupExitPoll):
	case <-ctx.Done():
		return false
	}
	if alive == nil {
		return true
	}

	deadline := time.Now().Add(cleanupExitWait)
	for {
		probeCtx, cancel := context.WithTimeout(ctx, cleanupProbeTimeout)
		running, err := alive(probeCtx, name)
		cancel()
		if err != nil {
			// Unreachable is not the same as alive, and it is not evidence worth
			// sparing a run over: the agent cannot be talked to, so tear it down
			// exactly as this listener always did.
			logger.Debug().Err(err).Str("agent", name).
				Msg("agent cleanup: cannot probe claude, treating the run as ended")
			return true
		}
		if !running {
			return true
		}
		if time.Now().After(deadline) {
			// The run outlived the exit event, so the event was somebody else's —
			// a subagent's. Leave it working; its own ending will arrive later,
			// and a silent death is the zombie sweep's to catch.
			logger.Info().Str("agent", name).Dur("waited", cleanupExitWait).
				Msg("agent cleanup: claude still running past the exit event, leaving the run alone")
			return false
		}
		select {
		case <-time.After(cleanupExitPoll):
		case <-ctx.Done():
			return false
		}
	}
}
