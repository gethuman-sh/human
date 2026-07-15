package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type mockSweeper struct {
	agents     []AgentInfo
	processUp  map[string]bool // containerID → running
	deleted    []string
	processErr map[string]error
	deleteErr  map[string]error // agent name → forced delete failure
	listErr    error
}

func (m *mockSweeper) RunningAgents() ([]AgentInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.agents, nil
}

func (m *mockSweeper) IsProcessRunning(_ context.Context, containerID string, _ string) (bool, error) {
	if m.processErr != nil {
		if err, ok := m.processErr[containerID]; ok {
			return false, err
		}
	}
	return m.processUp[containerID], nil
}

func (m *mockSweeper) DeleteAgent(_ context.Context, name string) error {
	if m.deleteErr != nil {
		if err, ok := m.deleteErr[name]; ok {
			return err
		}
	}
	m.deleted = append(m.deleted, name)
	return nil
}

func TestSweepZombieAgents_CleansOrphaned(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": false},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"agent-1"}, s.deleted)
}

func TestSweepZombieAgents_SkipsHealthy(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": true},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsGracePeriod(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-3 * time.Second)},
		},
		processUp: map[string]bool{"c1": false},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsEmptyContainerID(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_MixedAgents(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "healthy", ContainerID: "c1", CreatedAt: time.Now().Add(-5 * time.Minute)},
			{Name: "zombie", ContainerID: "c2", CreatedAt: time.Now().Add(-3 * time.Minute)},
			{Name: "new", ContainerID: "c3", CreatedAt: time.Now().Add(-3 * time.Second)},
		},
		processUp: map[string]bool{"c1": true, "c2": false, "c3": false},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"zombie"}, s.deleted)
}

func TestRunAgentZombieSweep_NilSweeper(t *testing.T) {
	// Should return immediately without panic.
	RunAgentZombieSweep(context.Background(), nil, nil, zerolog.Nop())
}

// SC-206: a reaped board agent died without firing hook events, so the sweep
// is the only place that knows the exit happened. It must notify the injected
// seam — otherwise no failure marker is ever posted and the board card spins
// forever (the SC-204 incident shape: container up 2m, claude process gone).
func TestSweepZombieAgents_NotifiesOnReap(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "board-204-implementation", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": false},
	}
	var reaped []string
	onReaped := func(name string) { reaped = append(reaped, name) }

	sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())

	assert.Equal(t, []string{"board-204-implementation"}, s.deleted)
	assert.Equal(t, []string{"board-204-implementation"}, reaped)
}

func TestSweepZombieAgents_NoNotifyWhenDeleteFails(t *testing.T) {
	// A failed delete is retried on the next tick; notifying now would mark
	// the stage failed while the container may still be recoverable.
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "board-204-implementation", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": false},
		deleteErr: map[string]error{"board-204-implementation": assertErr{}},
	}
	var reaped []string
	onReaped := func(name string) { reaped = append(reaped, name) }

	sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())

	assert.Empty(t, reaped)
}

func TestSweepZombieAgents_NilOnReaped(t *testing.T) {
	// Callers without a hook store pass nil — the reap must still happen.
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "board-204-implementation", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": false},
	}

	sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"board-204-implementation"}, s.deleted)
}
