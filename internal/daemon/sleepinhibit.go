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

	loop := &sleepInhibitLoop{
		lister:    lister,
		inhibitor: inhibitor,
		enabled:   enabled,
		logger:    logger,
	}
	defer loop.releaseIfHeld()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			loop.tick()
		}
	}
}

// sleepInhibitLoop carries the block-held / failure-logged state between ticks
// so the per-tick decision logic can live in small named functions.
type sleepInhibitLoop struct {
	lister    RunningAgentLister
	inhibitor SleepInhibitor
	enabled   func() bool
	logger    zerolog.Logger

	release       func() error // non-nil while the block is held
	loggedFailure bool         // suppresses repeat error spam within one failure streak
}

func (l *sleepInhibitLoop) tick() {
	if !l.enabled() {
		l.releaseIfHeld()
		l.loggedFailure = false
		return
	}
	agents, err := l.lister.RunningAgents()
	if err != nil {
		// Keep whatever block we currently hold: a transient list error must
		// not release protection out from under a live run.
		l.logger.Warn().Err(err).Msg("sleep inhibitor: cannot list running agents")
		return
	}
	running := len(agents) > 0
	switch {
	case running && l.release == nil:
		l.acquire(len(agents))
	case !running && l.release != nil:
		l.releaseIfHeld()
	}
}

func (l *sleepInhibitLoop) acquire(agentCount int) {
	release, err := l.inhibitor.Acquire("human", "human agents running — deferring suspend")
	if err != nil {
		if !l.loggedFailure {
			l.logger.Error().Err(err).Msg("could not acquire sleep inhibitor — agent runs are exposed to system suspend")
			l.loggedFailure = true
		}
		return
	}
	l.release = release
	l.loggedFailure = false
	l.logger.Info().Int("agents", agentCount).Msg("acquired sleep inhibitor: agents running")
}

func (l *sleepInhibitLoop) releaseIfHeld() {
	if l.release == nil {
		return
	}
	if err := l.release(); err != nil {
		l.logger.Warn().Err(err).Msg("releasing sleep inhibitor failed")
	}
	l.release = nil
	l.logger.Info().Msg("released sleep inhibitor: no agents running")
}
