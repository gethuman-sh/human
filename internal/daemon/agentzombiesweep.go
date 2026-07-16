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
)

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

	// seenClaude records, per agent name, whether the sweep has ever observed a
	// live claude process for it. An idle-by-design agent that has never been
	// seen with claude is spared; once claude is observed, a later absence still
	// reaps the agent — preserving the crashed-agent contract (SC-236). Kept
	// in memory across ticks; a daemon restart resets it, which is safe: an idle
	// agent is spared again by its Idle flag, and a crashed prompt-driven agent
	// is reaped on the first tick because its Idle flag is false.
	seenClaude := map[string]bool{}

	ticker := time.NewTicker(zombieSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepZombieAgents(ctx, sweeper, seenClaude, onReaped, logger)
		}
	}
}

func sweepZombieAgents(ctx context.Context, sweeper AgentZombieSweeper, seenClaude map[string]bool, onReaped func(agentName string), logger zerolog.Logger) {
	agents, err := sweeper.RunningAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("zombie sweep: failed to list agents")
		return
	}

	live := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		live[a.Name] = struct{}{}

		if a.ContainerID == "" {
			continue
		}
		if time.Since(a.CreatedAt) < zombieGracePeriod {
			continue
		}

		running, err := sweeper.IsProcessRunning(ctx, a.ContainerID, "claude")
		if err != nil {
			// Container may have been removed between list and check.
			logger.Debug().Err(err).Str("agent", a.Name).Msg("zombie sweep: process check failed")
			continue
		}
		if running {
			// Record the observation so a later absence still reaps this agent,
			// even one flagged idle — this is what preserves the crashed-agent
			// contract for agents that once ran claude (SC-236).
			seenClaude[a.Name] = true
			continue
		}

		// claude is absent. Spare an idle-by-design agent that has never been
		// observed running claude: it is idle on purpose, not a zombie (SC-236).
		if a.Idle && !seenClaude[a.Name] {
			continue
		}

		logger.Info().Str("agent", a.Name).Msg("zombie sweep: cleaning orphaned agent")
		deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := sweeper.DeleteAgent(deleteCtx, a.Name); err != nil {
			logger.Warn().Err(err).Str("agent", a.Name).Msg("zombie sweep: cleanup failed")
		} else if onReaped != nil {
			// Notify only on a completed reap: a failed delete is retried on
			// the next tick and must not mark the stage failed prematurely.
			onReaped(a.Name)
		}
		cancel()
	}

	// Drop observations for agents that no longer exist so the map stays bounded
	// by the live agent set across the sweep goroutine's lifetime.
	for name := range seenClaude {
		if _, ok := live[name]; !ok {
			delete(seenClaude, name)
		}
	}
}
