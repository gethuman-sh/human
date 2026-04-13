package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type mockSweeper struct {
	agents      []AgentInfo
	processUp   map[string]bool // containerID → running
	deleted     []string
	processErr  map[string]error
	listErr     error
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

	sweepZombieAgents(context.Background(), s, zerolog.Nop())

	assert.Equal(t, []string{"agent-1"}, s.deleted)
}

func TestSweepZombieAgents_SkipsHealthy(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": true},
	}

	sweepZombieAgents(context.Background(), s, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsGracePeriod(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-3 * time.Second)},
		},
		processUp: map[string]bool{"c1": false},
	}

	sweepZombieAgents(context.Background(), s, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsEmptyContainerID(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{},
	}

	sweepZombieAgents(context.Background(), s, zerolog.Nop())

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

	sweepZombieAgents(context.Background(), s, zerolog.Nop())

	assert.Equal(t, []string{"zombie"}, s.deleted)
}

func TestRunAgentZombieSweep_NilSweeper(t *testing.T) {
	// Should return immediately without panic.
	RunAgentZombieSweep(context.Background(), nil, zerolog.Nop())
}
