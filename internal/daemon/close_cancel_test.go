package daemon

import (
	"context"
	stderrors "errors"
	"sort"
	"testing"

	"github.com/rs/zerolog"
)

func TestAgentsForPMKey_MatchesEveryStageOfTheKey(t *testing.T) {
	names := []string{
		agentNameFor("SC-1698", BoardImplementation),
		agentNameFor("SC-1698", BoardVerification),
		agentNameFor("SC-99", BoardImplementation),
		"not-a-board-agent",
		"board-malformed",
	}
	got := AgentsForPMKey(names, "SC-1698")
	sort.Strings(got)
	want := []string{
		agentNameFor("SC-1698", BoardImplementation),
		agentNameFor("SC-1698", BoardVerification),
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestAgentsForPMKey_MatchesViaSanitizedKey(t *testing.T) {
	// The raw key carries a character the name-encoding replaces; matching must
	// go through the same sanitize() the launcher used, not raw equality.
	name := agentNameFor("SC/1698", BoardImplementation)
	if got := AgentsForPMKey([]string{name}, "SC/1698"); len(got) != 1 || got[0] != name {
		t.Fatalf("sanitized match failed: got %v", got)
	}
}

func TestStopAgentsForPMKey_StopsOnlyTheKeysAgents(t *testing.T) {
	names := []string{
		agentNameFor("SC-1698", BoardImplementation),
		agentNameFor("SC-1698", BoardVerification),
		agentNameFor("SC-99", BoardImplementation),
	}
	var stopped []string
	n, err := StopAgentsForPMKey(context.Background(), "SC-1698",
		func() ([]string, error) { return names, nil },
		func(_ context.Context, name string) error { stopped = append(stopped, name); return nil },
		zerolog.Nop())
	if err != nil {
		t.Fatalf("a clean stop must report no error, got %v", err)
	}
	if n != 2 {
		t.Fatalf("stopped count = %d, want 2", n)
	}
	if len(stopped) != 2 {
		t.Fatalf("stopped %v, want the two SC-1698 agents", stopped)
	}
}

func TestStopAgentsForPMKey_NoAgentClaimingKeyIsACleanNilError(t *testing.T) {
	// The common case: closing a ticket with no live run must report success so
	// the caller's transition is allowed to proceed.
	names := []string{agentNameFor("SC-99", BoardImplementation)}
	n, err := StopAgentsForPMKey(context.Background(), "SC-1698",
		func() ([]string, error) { return names, nil },
		func(_ context.Context, _ string) error { return nil },
		zerolog.Nop())
	if n != 0 || err != nil {
		t.Fatalf("no matching agent must be a clean no-op (n=%d err=%v)", n, err)
	}
}

func TestStopAgentsForPMKey_ContinuesPastAStopFailure(t *testing.T) {
	names := []string{
		agentNameFor("SC-1698", BoardImplementation),
		agentNameFor("SC-1698", BoardVerification),
	}
	var stopped []string
	n, err := StopAgentsForPMKey(context.Background(), "SC-1698",
		func() ([]string, error) { return names, nil },
		func(_ context.Context, name string) error {
			stopped = append(stopped, name)
			if name == names[0] {
				return stderrors.New("boom")
			}
			return nil
		},
		zerolog.Nop())
	if n != 1 {
		t.Fatalf("stopped count = %d, want 1 (one failed, one succeeded)", n)
	}
	// The failed stop is the "agent refuses to die" case: it must be reported so
	// the caller leaves the card open rather than closing over the live run.
	if err == nil {
		t.Fatalf("a stop failure must be reported so the close is gated, got nil error")
	}
	if len(stopped) != 2 {
		t.Fatalf("attempted %v, want both agents attempted despite the failure", stopped)
	}
}

func TestStopAgentsForPMKey_LeavesRunsAloneWhenLivenessProbeFails(t *testing.T) {
	stopCalled := false
	n, err := StopAgentsForPMKey(context.Background(), "SC-1698",
		func() ([]string, error) { return nil, stderrors.New("probe blip") },
		func(_ context.Context, _ string) error { stopCalled = true; return nil },
		zerolog.Nop())
	if n != 0 || stopCalled {
		t.Fatalf("a liveness-probe failure must stop nothing (n=%d stopCalled=%v)", n, stopCalled)
	}
	// Unable to confirm the run is stopped: the close must be gated so the card
	// stays open and the reconcile net can reach any orphaned agent.
	if err == nil {
		t.Fatalf("a liveness-probe failure must be reported so the close is gated, got nil error")
	}
}

func TestStopAgentsForPMKey_NilDepsAreNoOp(t *testing.T) {
	n, err := StopAgentsForPMKey(context.Background(), "SC-1698", nil, nil, zerolog.Nop())
	if n != 0 || err != nil {
		t.Fatalf("nil deps must be a clean no-op (n=%d err=%v)", n, err)
	}
}
