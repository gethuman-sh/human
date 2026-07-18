package daemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// AgentInfo holds the minimum metadata needed by the zombie sweep.
type AgentInfo struct {
	Name        string
	ContainerID string
	CreatedAt   time.Time
	// Idle marks an agent started without a prompt (bare `human agent start`),
	// which by design never launches a claude process. Such an agent is
	// indistinguishable from a crashed one by process-liveness alone, so the
	// sweep must not reap it until claude has actually been observed running
	// for it — otherwise a deliberately idle agent is killed within seconds
	// of coming up (SC-236).
	Idle bool
}

// AgentZombieSweeper checks for orphaned agent containers whose main
// process has exited but the container is still running.
type AgentZombieSweeper interface {
	RunningAgents() ([]AgentInfo, error)
	IsProcessRunning(ctx context.Context, containerID string, process string) (bool, error)
	DeleteAgent(ctx context.Context, name string) error
}

const (
	zombieSweepInterval = 5 * time.Second
	// Grace period before a container without Claude is considered a zombie.
	// Allows time for Claude to start after the container comes up.
	zombieGracePeriod = 10 * time.Second
	// A liveness check can fail transiently (a one-shot race where the container
	// is removed between list and check). But a post-suspend Docker/exec
	// disruption fails *persistently* every tick — left unbounded, it skips the
	// reap forever and the board card spins at "reviewing…" (SC-263). After this
	// many consecutive failures the agent is presumed unreachable-and-dead and
	// reaped. At zombieSweepInterval this is ~15s, matching the wake-recovery
	// expectation.
	zombieMaxProcessCheckFailures = 3
	// A single reap must never park the sweep's one goroutine indefinitely: a
	// stalled CopyTranscript inside DeleteAgent would otherwise stop every later
	// agent from ever being reaped (SC-427). Past this deadline the reap is
	// abandoned to the background and the loop advances so the next tick keeps
	// reaping other dead agents. Generous relative to the 30s delete timeout so
	// a healthy-but-slow reap still completes inline.
	zombieReapHardDeadline = 45 * time.Second
)

// zombieSweep carries the sweep's cross-tick memory. checkFailures counts
// consecutive liveness-check errors per agent so a persistent (not one-shot)
// failure is bounded and escalated into a reap rather than skipped
// indefinitely (SC-263). seenClaude records whether claude was ever observed
// running per agent so an idle-by-design agent is spared until then (SC-236).
// A daemon restart resets both, which is safe: an idle agent is spared again
// by its Idle flag, and a crashed prompt-driven agent is reaped on the first
// tick because its Idle flag is false.
type zombieSweep struct {
	checkFailures map[string]int
	seenClaude    map[string]bool
	// reapHardDeadline bounds a single reap so one hung delete cannot starve the
	// sweep loop (SC-427). Injectable so tests can shrink it below the
	// production default.
	reapHardDeadline time.Duration
}

func newZombieSweep() *zombieSweep {
	return &zombieSweep{
		checkFailures:    make(map[string]int),
		seenClaude:       make(map[string]bool),
		reapHardDeadline: zombieReapHardDeadline,
	}
}

// RunAgentZombieSweep periodically checks for agent containers that are
// still running but have no Claude process. This catches cases where Claude
// failed to start, crashed without firing hook events, or the user killed
// the tmux pane.
//
// A reaped agent by definition died without emitting hook events, so the
// caller uses onReaped to synthesize the exit signal that hook-driven paths
// get for free — otherwise the board failure watcher never learns of the
// exit and the board card spins forever (SC-206). nil disables notification.
// The AgentZombieSweeper interface is deliberately NOT widened: the sweep's
// job is to reap zombies and report the fact, not to post markers itself.
func RunAgentZombieSweep(ctx context.Context, sweeper AgentZombieSweeper, onReaped func(agentName string), logger zerolog.Logger) {
	if sweeper == nil {
		return
	}

	logger.Info().Msg("agent zombie sweep started")

	ticker := time.NewTicker(zombieSweepInterval)
	defer ticker.Stop()

	sweep := newZombieSweep()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep.sweepZombieAgents(ctx, sweeper, onReaped, logger)
		}
	}
}

func (z *zombieSweep) sweepZombieAgents(ctx context.Context, sweeper AgentZombieSweeper, onReaped func(agentName string), logger zerolog.Logger) {
	agents, err := sweeper.RunningAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("zombie sweep: failed to list agents")
		return
	}

	z.prune(agents)

	for _, a := range agents {
		if a.ContainerID == "" {
			continue
		}
		if time.Since(a.CreatedAt) < zombieGracePeriod {
			continue
		}

		running, err := sweeper.IsProcessRunning(ctx, a.ContainerID, "claude")
		if err != nil {
			// A one-shot error (container removed between list and check) is
			// tolerated by skipping this tick. But a persistent post-suspend
			// Docker/exec disruption recurs every tick — bounding it forces an
			// escalation to reap instead of an indefinite skip (SC-263).
			z.checkFailures[a.Name]++
			if z.checkFailures[a.Name] <= zombieMaxProcessCheckFailures {
				logger.Warn().Err(err).Str("agent", a.Name).Int("failures", z.checkFailures[a.Name]).
					Msg("zombie sweep: process check failed, will retry")
				continue
			}
			logger.Error().Err(err).Str("agent", a.Name).Int("failures", z.checkFailures[a.Name]).
				Msg("zombie sweep: process check failing persistently, presuming agent dead and reaping")
			// Fall through to the reap gate: the agent is unreachable and
			// presumed dead. The idle-never-seen spare below still applies —
			// SC-236's contract is absolute, even under a broken Docker.
		} else {
			// A successful check clears the transient-failure streak.
			delete(z.checkFailures, a.Name)
			if running {
				// Record the observation so a later absence still reaps this
				// agent, even one flagged idle — this is what preserves the
				// crashed-agent contract for agents that once ran claude (SC-236).
				z.seenClaude[a.Name] = true
				continue
			}
		}

		// claude is absent (or the container unreachable for good). Spare an
		// idle-by-design agent that has never been observed running claude: it
		// is idle on purpose, not a zombie (SC-236).
		if a.Idle && !z.seenClaude[a.Name] {
			continue
		}

		z.reap(ctx, sweeper, a.Name, onReaped, logger)
	}
}

// reap deletes one agent under a hard deadline that the sweep loop can never be
// starved past. DeleteAgent runs on its own goroutine (with the existing 30s
// delete timeout); if it exceeds reapHardDeadline the reap is abandoned to the
// background and reap returns so the loop keeps reaping other dead agents
// (SC-427). The abandoned goroutine still finishes into the buffered channel,
// and the agent's cross-tick memory is intentionally left so a later tick
// retries it.
func (z *zombieSweep) reap(ctx context.Context, sweeper AgentZombieSweeper, name string, onReaped func(agentName string), logger zerolog.Logger) {
	logger.Info().Str("agent", name).Msg("zombie sweep: cleaning orphaned agent")

	deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	// Buffered so an abandoned reap goroutine can always send and exit even
	// after this function has returned — no goroutine leak. The goroutine owns
	// cancel(): it fires once DeleteAgent returns, whether inline or after the
	// loop has abandoned it, so the delete ctx is always released.
	resultCh := make(chan error, 1)
	go func() {
		defer cancel()
		resultCh <- sweeper.DeleteAgent(deleteCtx, name)
	}()

	timer := time.NewTimer(z.reapHardDeadline)
	defer timer.Stop()

	select {
	case err := <-resultCh:
		if err != nil {
			logger.Warn().Err(err).Str("agent", name).Msg("zombie sweep: cleanup failed")
			return
		}
		// A completed reap clears the agent's cross-tick memory so a same-named
		// future agent starts fresh.
		delete(z.checkFailures, name)
		delete(z.seenClaude, name)
		if onReaped != nil {
			// Notify only on a completed reap: a failed delete is retried on the
			// next tick and must not mark the stage failed prematurely.
			onReaped(name)
		}
	case <-timer.C:
		// Abandon the hung reap to keep the single sweep goroutine alive. The
		// background goroutine keeps its own 30s delete budget and cancels the
		// ctx when it eventually returns; memory is left so the next tick
		// retries this agent.
		logger.Warn().Str("agent", name).
			Msg("zombie sweep: reap exceeded hard deadline, abandoning to keep the loop alive")
	case <-ctx.Done():
		// The whole sweep is shutting down; the goroutine's deferred cancel
		// releases the delete ctx once DeleteAgent unwinds.
	}
}

// prune drops cross-tick memory for agents that no longer exist, so the maps
// stay bounded by the live agent set across the sweep goroutine's lifetime.
func (z *zombieSweep) prune(agents []AgentInfo) {
	if len(z.checkFailures) == 0 && len(z.seenClaude) == 0 {
		return
	}
	live := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		live[a.Name] = struct{}{}
	}
	for name := range z.checkFailures {
		if _, ok := live[name]; !ok {
			delete(z.checkFailures, name)
		}
	}
	for name := range z.seenClaude {
		if _, ok := live[name]; !ok {
			delete(z.seenClaude, name)
		}
	}
}
