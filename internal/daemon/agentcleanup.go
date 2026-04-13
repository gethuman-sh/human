package daemon

import (
	"context"
	"time"

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

// RunAgentCleanup watches for SessionEnd hook events from devcontainer agents
// and automatically stops the container and removes the worktree.
func RunAgentCleanup(ctx context.Context, store *HookEventStore, cleaner AgentCleaner, logger zerolog.Logger) {
	if store == nil || cleaner == nil {
		return
	}

	ch := store.Subscribe()
	defer store.Unsubscribe(ch)

	logger.Info().Msg("agent cleanup listener started")

	cleaned := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			for _, evt := range store.RecentEvents() {
				if evt.AgentName == "" {
					continue
				}
				if evt.EventName != "Stop" && evt.EventName != "SessionEnd" && evt.EventName != "StopFailure" {
					continue
				}
				if cleaned[evt.AgentName] {
					continue
				}
				cleaned[evt.AgentName] = true
				go func(name string) {
					// Wait for Claude to fully exit before stopping the container.
					select {
					case <-time.After(1 * time.Second):
					case <-ctx.Done():
						return
					}
					logger.Info().Str("agent", name).Msg("auto-cleaning agent after session end")
					deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					if err := cleaner.DeleteAgent(deleteCtx, name); err != nil {
						logger.Warn().Err(err).Str("agent", name).Msg("agent cleanup failed")
					}
				}(evt.AgentName)
			}
		}
	}
}
