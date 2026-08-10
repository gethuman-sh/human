package daemon

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAgentLister struct {
	agents []AgentInfo
	err    error
}

func (f fakeAgentLister) RunningAgents() ([]AgentInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}

func TestRepairAgentIPs_MapsAnUnmappedRunningAgent(t *testing.T) {
	lister := fakeAgentLister{agents: []AgentInfo{{Name: "board-SC-1-implementation", ContainerID: "c1"}}}
	reg := NewAgentIPRegistry()
	pending := NewPendingModelRequests()
	resolve := func(ctx context.Context, containerID string) (string, error) { return "172.17.0.9", nil }

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, map[string]bool{}, zerolog.Nop())

	name, ok := reg.AgentFor("172.17.0.9")
	require.True(t, ok)
	assert.Equal(t, "board-SC-1-implementation", name)
}

func TestRepairAgentIPs_SkipsAlreadyMappedAgents(t *testing.T) {
	name := "board-SC-1-implementation"
	lister := fakeAgentLister{agents: []AgentInfo{{Name: name, ContainerID: "c1"}}}
	reg := NewAgentIPRegistry()
	reg.Register("172.17.0.9", name)
	pending := NewPendingModelRequests()
	called := false
	resolve := func(ctx context.Context, containerID string) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, map[string]bool{}, zerolog.Nop())

	assert.False(t, called, "an already-mapped agent must not be re-resolved")
}

func TestRepairAgentIPs_EmptyIPWarnsOnceAndLeavesUnmapped(t *testing.T) {
	name := "board-SC-1-implementation"
	lister := fakeAgentLister{agents: []AgentInfo{{Name: name, ContainerID: "c1"}}}
	reg := NewAgentIPRegistry()
	pending := NewPendingModelRequests()
	resolve := func(ctx context.Context, containerID string) (string, error) { return "", nil }

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	warned := map[string]bool{}

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, warned, logger)
	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, warned, logger)

	assert.False(t, reg.Mapped(name))
	count := 0
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if bytes.Contains(line, []byte("no address for a running agent")) {
			count++
		}
	}
	assert.Equal(t, 1, count, "the warning must be logged exactly once across repeated passes")
}

func TestRepairAgentIPs_ListErrorDoesNotPrune(t *testing.T) {
	name := "board-SC-1-implementation"
	reg := NewAgentIPRegistry()
	reg.Register("172.17.0.9", name)
	pending := NewPendingModelRequests()
	lister := fakeAgentLister{err: errors.New("docker unreachable")}
	resolve := func(ctx context.Context, containerID string) (string, error) { return "", nil }

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, map[string]bool{}, zerolog.Nop())

	assert.True(t, reg.Mapped(name), "a failed list must never prune the registry")
}

func TestRepairAgentIPs_PrunesMappingsForGoneAgents(t *testing.T) {
	reg := NewAgentIPRegistry()
	reg.Register("172.17.0.9", "board-SC-1-planning")
	pending := NewPendingModelRequests()
	lister := fakeAgentLister{agents: nil}
	resolve := func(ctx context.Context, containerID string) (string, error) { return "", nil }

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, map[string]bool{}, zerolog.Nop())

	assert.False(t, reg.Mapped("board-SC-1-planning"))
}

// A model-request mark held before the mapping existed must be replayed the
// moment the repair pass installs the mapping — the daemon-restart hole
// review round 1 found (SC-3853).
func TestRepairAgentIPs_ReplaysPendingMarkOnceMappingArrives(t *testing.T) {
	name := "board-SC-3853-implementation"
	lister := fakeAgentLister{agents: []AgentInfo{{Name: name, ContainerID: "c1"}}}
	reg := NewAgentIPRegistry()
	pending := NewPendingModelRequests()
	inflight := NewInflightModelRequests()
	pending.Hold("172.17.0.9", 1)
	resolve := func(ctx context.Context, containerID string) (string, error) { return "172.17.0.9", nil }

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, inflight, map[string]bool{}, zerolog.Nop())

	assert.True(t, inflight.Outstanding(name), "a mark held before the mapping existed must be replayed")
}

func TestRepairAgentIPs_PrunesStalePendingHolds(t *testing.T) {
	lister := fakeAgentLister{agents: nil}
	reg := NewAgentIPRegistry()
	pending := NewPendingModelRequests()
	pending.Hold("172.17.0.9", 1)
	// Backdate the hold past the max age directly, mirroring Prune's own test.
	pending.mu.Lock()
	mark := pending.byHost["172.17.0.9"]
	mark.markedAt = time.Now().Add(-pendingHoldMaxAge - time.Minute)
	pending.byHost["172.17.0.9"] = mark
	pending.mu.Unlock()
	resolve := func(ctx context.Context, containerID string) (string, error) { return "", nil }

	repairAgentIPs(context.Background(), lister, resolve, reg, pending, nil, map[string]bool{}, zerolog.Nop())

	inflight := NewInflightModelRequests()
	pending.Replay("172.17.0.9", "board-SC-1-implementation", inflight)
	assert.False(t, inflight.Outstanding("board-SC-1-implementation"), "the stale hold must have been pruned")
}

func TestRunAgentIPRepair_NilDepsNoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		RunAgentIPRepair(context.Background(), nil, nil, nil, nil, nil, zerolog.Nop())
	})
}
