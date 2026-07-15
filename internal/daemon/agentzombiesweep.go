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

	ticker := time.NewTicker(zombieSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepZombieAgents(ctx, sweeper, onReaped, logger)
		}
	}
}

func sweepZombieAgents(ctx context.Context, sweeper AgentZombieSweeper, onReaped func(agentName string), logger zerolog.Logger) {
	agents, err := sweeper.RunningAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("zombie sweep: failed to list agents")
		return
	}

	for _, a := range agents {
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
}
