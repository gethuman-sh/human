package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// openCards builds the reconcile input for keys that are still on the board.
func openCards(keys ...string) []ReconcileCard {
	cards := make([]ReconcileCard, 0, len(keys))
	for _, k := range keys {
		cards = append(cards, ReconcileCard{Key: k})
	}
	return cards
}

// closedKeys builds a probe that confirms exactly the named keys as closed.
func closedKeys(keys ...string) ClosedTicketProbe {
	closed := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		closed[k] = struct{}{}
	}
	return func(_ context.Context, key string) (bool, error) {
		_, ok := closed[key]
		return ok, nil
	}
}

// The defect this fixes: a ticket closed OUTSIDE the board (tracker web UI,
// `human close`, a teammate) never passes through the close gate, and the
// reconcile net enumerates open cards only — so its agent kept working
// invisibly against a closed ticket forever.
func TestReconcileOrphanedAgents_StopsAgentOnTicketClosedOutsideTheBoard(t *testing.T) {
	var stopped []string

	n := reconcileOrphanedAgents(context.Background(), openCards("SC-2"),
		liveAgents("board-SC-1-implementation"), closedKeys("SC-1"),
		func(name string) error { stopped = append(stopped, name); return nil },
		zerolog.Nop())

	require.Equal(t, 1, n)
	require.Equal(t, []string{"board-SC-1-implementation"}, stopped,
		"the run orphaned on the closed ticket must be stopped")
}

// One card can hold several stage agents; a single probe covers them all.
func TestReconcileOrphanedAgents_StopsEveryStageOfTheClosedTicket(t *testing.T) {
	var stopped []string
	probes := 0
	probe := func(_ context.Context, key string) (bool, error) {
		probes++
		return key == "SC-1", nil
	}

	n := reconcileOrphanedAgents(context.Background(), nil,
		liveAgents("board-SC-1-implementation", "board-SC-1-verification"), probe,
		func(name string) error { stopped = append(stopped, name); return nil },
		zerolog.Nop())

	require.Equal(t, 2, n)
	require.ElementsMatch(t, []string{"board-SC-1-implementation", "board-SC-1-verification"}, stopped)
	require.Equal(t, 1, probes, "the stages of one card share a single probe round-trip")
}

// An agent whose card is still on the board is dismissed without ever reaching
// the tracker — the sweep must cost nothing on a healthy board.
func TestReconcileOrphanedAgents_OpenCardIsNeverProbed(t *testing.T) {
	probed := false
	probe := func(context.Context, string) (bool, error) {
		probed = true
		return true, nil
	}

	n := reconcileOrphanedAgents(context.Background(), openCards("SC-1"),
		liveAgents("board-SC-1-implementation"), probe,
		func(string) error { t.Fatal("a live run on an open card must never be stopped"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
	require.False(t, probed, "an agent matching an open card needs no tracker call")
}

// Absence from the open-card list is NOT proof of a close: a flaky per-ticket
// comment fetch drops a healthy card too, so only a confirmed close may kill.
func TestReconcileOrphanedAgents_AbsentButOpenTicketIsLeftRunning(t *testing.T) {
	n := reconcileOrphanedAgents(context.Background(), nil,
		liveAgents("board-SC-1-implementation"), closedKeys(),
		func(string) error { t.Fatal("a ticket that is merely absent must not stop its run"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
}

// A probe that cannot answer resolves to "leave it running": killing live work
// on absent evidence is the one failure this must never risk.
func TestReconcileOrphanedAgents_ProbeErrorLeavesTheRunAlone(t *testing.T) {
	probe := func(context.Context, string) (bool, error) {
		return false, errors.New("tracker unreachable")
	}

	n := reconcileOrphanedAgents(context.Background(), nil,
		liveAgents("board-SC-1-implementation"), probe,
		func(string) error { t.Fatal("an unconfirmed close must not stop a run"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
}

// A failed stop must not be counted as a stop, and must not abort the sweep for
// the agents behind it.
func TestReconcileOrphanedAgents_FailedStopIsNotCountedAndDoesNotAbort(t *testing.T) {
	var attempted []string
	stop := func(name string) error {
		attempted = append(attempted, name)
		if name == "board-SC-1-implementation" {
			return errors.New("container refused to die")
		}
		return nil
	}

	n := reconcileOrphanedAgents(context.Background(), nil,
		liveAgents("board-SC-1-implementation", "board-SC-1-verification"),
		closedKeys("SC-1"), stop, zerolog.Nop())

	require.Equal(t, 1, n, "only the confirmed stop counts")
	require.Len(t, attempted, 2, "a failed stop must not abort the remaining agents")
}

// Names that are not board agents carry no PM key and must be ignored rather
// than probed as if they were one.
func TestReconcileOrphanedAgents_NonBoardAgentsAreIgnored(t *testing.T) {
	probe := func(context.Context, string) (bool, error) {
		t.Fatal("a non-board container must never be probed")
		return false, nil
	}

	n := reconcileOrphanedAgents(context.Background(), nil,
		liveAgents("some-other-container", "board-"), probe,
		func(string) error { t.Fatal("a non-board container must never be stopped"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
}

// A liveness probe that cannot enumerate the runs must not be read as "nothing
// is running" — there is nothing to reconcile against.
func TestReconcileOrphanedAgents_ListErrorIsNotAnEmptyFleet(t *testing.T) {
	lister := func() ([]string, error) { return nil, errors.New("docker down") }

	n := reconcileOrphanedAgents(context.Background(), nil, lister, closedKeys("SC-1"),
		func(string) error { t.Fatal("nothing may be stopped without a live-agent list"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
}

// nil deps disable the sweep, matching the package's convention for optional
// dependencies.
func TestReconcileOrphanedAgents_NilDepsDisableTheSweep(t *testing.T) {
	stop := func(string) error { t.Fatal("a disabled sweep must stop nothing"); return nil }

	require.Zero(t, reconcileOrphanedAgents(context.Background(), nil, nil, closedKeys("SC-1"), stop, zerolog.Nop()))
	require.Zero(t, reconcileOrphanedAgents(context.Background(), nil, liveAgents("board-SC-1-implementation"), nil, stop, zerolog.Nop()))
	require.Zero(t, reconcileOrphanedAgents(context.Background(), nil, liveAgents("board-SC-1-implementation"), closedKeys("SC-1"), nil, zerolog.Nop()))
}

// The open-card match runs through the same sanitize() the launcher used, so a
// key carrying encoded characters still matches its own card.
func TestReconcileOrphanedAgents_OpenCardMatchesThroughSanitizedKey(t *testing.T) {
	probe := func(context.Context, string) (bool, error) {
		t.Fatal("a sanitized key matching an open card needs no tracker call")
		return false, nil
	}

	n := reconcileOrphanedAgents(context.Background(), openCards("proj_1"),
		liveAgents(agentNameFor("proj_1", BoardImplementation)), probe,
		func(string) error { t.Fatal("a live run on an open card must never be stopped"); return nil },
		zerolog.Nop())

	require.Zero(t, n)
}
