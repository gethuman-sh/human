package daemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// SleepInhibitInterval is how often the inhibitor loop re-checks the running-
// agent set and the config toggle.
const SleepInhibitInterval = 5 * time.Second

// RunningAgentLister enumerates the agent containers currently running so the
// inhibitor loop can hold a suspend block for exactly as long as at least one
// is alive. Satisfied by the same dockerAgentSweeper the zombie sweep uses.
type RunningAgentLister interface {
	RunningAgents() ([]AgentInfo, error)
}

// SleepInhibitor takes and releases a system suspend/sleep block. Acquire
// returns a release closure; a nil error means the block is held until release
// is called. Implementations are platform-specific (logind on Linux; a no-op
// error elsewhere).
type SleepInhibitor interface {
	Acquire(who, why string) (release func() error, err error)
}

// RunSleepInhibitor holds a suspend inhibitor for exactly as long as the toggle
// is on AND at least one agent container is running, releasing it when either
// condition drops. Lights-out operation: the factory must keep running when the
// human walks away and the desktop auto-suspends. Blocks until ctx is cancelled.
//
// enabled is re-evaluated every tick so a settings-UI toggle applies without a
// daemon restart. If the block cannot be acquired it is logged loudly (once per
// failure streak) so the user knows runs are sleep-exposed.
func RunSleepInhibitor(
	ctx context.Context,
	lister RunningAgentLister,
	inhibitor SleepInhibitor,
	enabled func() bool,
	interval time.Duration,
	logger zerolog.Logger,
) {
	if lister == nil || inhibitor == nil || enabled == nil {
		return
	}

	logger.Info().Msg("sleep inhibitor started")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var release func() error // non-nil while the block is held
	var loggedFailure bool   // suppresses repeat error spam within one failure streak

	releaseIfHeld := func() {
		if release == nil {
			return
		}
		if err := release(); err != nil {
			logger.Warn().Err(err).Msg("releasing sleep inhibitor failed")
		}
		release = nil
		logger.Info().Msg("released sleep inhibitor: no agents running")
	}

	defer releaseIfHeld()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !enabled() {
				releaseIfHeld()
				loggedFailure = false
				continue
			}
			agents, err := lister.RunningAgents()
			if err != nil {
				// Keep whatever block we currently hold: a transient list
				// error must not release protection out from under a live run.
				logger.Warn().Err(err).Msg("sleep inhibitor: cannot list running agents")
				continue
			}
			running := len(agents) > 0
			switch {
			case running && release == nil:
				r, aerr := inhibitor.Acquire("human", "human agents running — deferring suspend")
				if aerr != nil {
					if !loggedFailure {
						logger.Error().Err(aerr).Msg("could not acquire sleep inhibitor — agent runs are exposed to system suspend")
						loggedFailure = true
					}
					continue
				}
				release = r
				loggedFailure = false
				logger.Info().Int("agents", len(agents)).Msg("acquired sleep inhibitor: agents running")
			case !running && release != nil:
				releaseIfHeld()
			}
		}
	}
}
