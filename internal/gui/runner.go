package gui

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

// startTimeout bounds one agent container start. First-ever dispatches
// build the devcontainer image, which can legitimately take minutes.
const startTimeout = 30 * time.Minute

// AgentStarter launches a headless agent. Injected so tests don't need
// Docker; production wires startAgentContainer.
type AgentStarter func(ctx context.Context, opts agent.StartOpts) error

// ManagerRunner is the production AgentRunner: it starts headless agents
// via agent.Manager (devcontainers) and stops them through the daemon's
// async cleanup path.
type ManagerRunner struct {
	// DaemonInfo is the running daemon's own info, passed through to
	// agent.Manager so dispatch can never trigger the daemon
	// restart path from inside the daemon (which would kill it).
	DaemonInfo *daemon.DaemonInfo
	Hooks      *daemon.HookEventStore
	Cleaner    daemon.AgentCleaner
	Logger     zerolog.Logger
	Start      AgentStarter
}

// Dispatch allocates an agent name and starts the container in the
// background; container builds take far too long to hold an HTTP request
// open. Lifecycle outcomes surface as hook events so WebSocket clients
// hear about them immediately.
func (r *ManagerRunner) Dispatch(_ context.Context, opts DispatchOpts) (string, error) {
	if opts.Prompt == "" {
		return "", errors.WithDetails("agent prompt must not be empty")
	}
	start := r.Start
	if start == nil {
		start = r.startAgentContainer
	}
	name := agent.NextName()

	go func() {
		// Deliberately detached from the request context: the dispatch
		// outlives the HTTP request that triggered it.
		ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
		defer cancel()
		err := start(ctx, agent.StartOpts{
			Name:      name,
			Prompt:    opts.Prompt,
			SkipPerms: true,
			Workspace: opts.ProjectDir,
		})
		if err != nil {
			r.Logger.Warn().Err(err).Str("agent", name).Msg("gui agent dispatch failed")
			r.appendEvent("AgentStartFailed", name)
			return
		}
		r.Logger.Info().Str("agent", name).Msg("gui agent dispatched")
		r.appendEvent("AgentStarted", name)
	}()

	return name, nil
}

// Stop mirrors the daemon's agent-stop-async route: drop the agent from
// the list immediately, tear the container down in the background.
func (r *ManagerRunner) Stop(_ context.Context, name string) error {
	if r.Cleaner == nil {
		return errors.WithDetails("agent cleanup not available")
	}
	containerID, err := r.Cleaner.DecommissionAgent(name)
	if err != nil {
		return errors.WrapWithDetails(err, "decommissioning agent", "name", name)
	}
	r.appendEvent("AgentStopped", name)

	if containerID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if stopErr := r.Cleaner.StopContainer(ctx, containerID); stopErr != nil {
				r.Logger.Warn().Err(stopErr).Str("agent", name).Msg("gui async container stop failed")
			}
		}()
	}
	return nil
}

// startAgentContainer is the production AgentStarter: a fresh Docker
// client per dispatch (dispatches are rare; holding a client open in the
// daemon for them buys nothing).
func (r *ManagerRunner) startAgentContainer(ctx context.Context, opts agent.StartOpts) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return errors.WrapWithDetails(err, "connecting to docker")
	}
	defer func() { _ = docker.Close() }()

	mgr := &agent.Manager{Docker: docker, DaemonInfo: r.DaemonInfo}
	_, err = mgr.Start(ctx, opts)
	return err
}

// appendEvent records an agent lifecycle event; the hook bridge converts
// it into a WebSocket push and the TCP subscribe route into a TUI signal.
func (r *ManagerRunner) appendEvent(eventName, agentName string) {
	if r.Hooks == nil {
		return
	}
	r.Hooks.Append(hookevents.Event{
		EventName: eventName,
		AgentName: agentName,
		Timestamp: time.Now().UTC(),
	})
}
