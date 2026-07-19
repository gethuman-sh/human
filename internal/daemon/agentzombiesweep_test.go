package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type mockSweeper struct {
	agents     []AgentInfo
	processUp  map[string]bool // containerID → running
	processErr map[string]error
	deleteErr  map[string]error // agent name → forced delete failure
	listErr    error

	// blockDelete gates DeleteAgent per agent: if a channel is registered for a
	// name, the call parks until that channel is closed (or the delete ctx is
	// cancelled), modelling a reap hung inside CopyTranscript.
	blockDelete map[string]chan struct{}

	mu      sync.Mutex
	deleted []string
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

func (m *mockSweeper) DeleteAgent(ctx context.Context, name string) error {
	if m.blockDelete != nil {
		if block, ok := m.blockDelete[name]; ok {
			select {
			case <-block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if m.deleteErr != nil {
		if err, ok := m.deleteErr[name]; ok {
			return err
		}
	}
	m.mu.Lock()
	m.deleted = append(m.deleted, name)
	m.mu.Unlock()
	return nil
}

// deletedNames returns a copy of the reaped-agent record under lock, since a
// blocked reap runs on its own goroutine and may append concurrently.
func (m *mockSweeper) deletedNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.deleted...)
}

func TestSweepZombieAgents_CleansOrphaned(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": false},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"agent-1"}, s.deleted)
}

func TestSweepZombieAgents_SkipsHealthy(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{"c1": true},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsGracePeriod(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-3 * time.Second)},
		},
		processUp: map[string]bool{"c1": false},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

func TestSweepZombieAgents_SkipsEmptyContainerID(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp: map[string]bool{},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

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

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"zombie"}, s.deleted)
}

func TestRunAgentZombieSweep_NilSweeper(t *testing.T) {
	// Should return immediately without panic.
	RunAgentZombieSweep(context.Background(), nil, nil, zerolog.Nop())
}

// SC-236: a deliberately idle agent (bare `human agent start NAME`, empty
// Prompt, so claude is never launched) must survive the sweep. It is
// indistinguishable from a crashed agent by process-liveness alone, so the
// sweep spares agents flagged idle that have never been observed running claude.
func TestSweepZombieAgents_SparesIdleNeverHadClaude(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "debug", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute), Idle: true},
		},
		processUp: map[string]bool{"c1": false},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
}

// SC-236 contract preservation: an agent that HAD claude and then lost it is a
// true zombie and must still be reaped — even if flagged idle — because the
// sweep observed claude running for it on a prior tick.
func TestSweepZombieAgents_ReapsAgentThatHadClaudeThenDied(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "crashed", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute), Idle: true},
		},
		processUp: map[string]bool{"c1": false},
	}
	// Prior observation: claude was seen running for this agent.
	sweep := newZombieSweep()
	sweep.seenClaude["crashed"] = true

	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"crashed"}, s.deleted)
}

// A prompt-driven (non-idle) agent whose claude is absent is reaped on the very
// first tick — no prior observation required, preserving pre-SC-236 behavior.
func TestSweepZombieAgents_ReapsNonIdleWithoutClaude(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "prompt-agent", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute), Idle: false},
		},
		processUp: map[string]bool{"c1": false},
	}

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"prompt-agent"}, s.deleted)
}

// An idle agent that IS currently running claude records the observation, so a
// later absence reaps it. Verifies seenClaude is populated on a live-claude tick.
func TestSweepZombieAgents_RecordsClaudeObservation(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "idle-then-ran", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute), Idle: true},
		},
		processUp: map[string]bool{"c1": true},
	}
	sweep := newZombieSweep()

	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Empty(t, s.deleted)
	assert.True(t, sweep.seenClaude["idle-then-ran"], "claude observation must be recorded")
}

// The cross-tick memory must not leak entries for agents that no longer exist.
func TestSweepZombieAgents_PrunesStaleMemory(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "still-here", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute), Idle: true},
		},
		processUp: map[string]bool{"c1": true},
	}
	sweep := newZombieSweep()
	sweep.seenClaude["gone"] = true
	sweep.seenClaude["still-here"] = true
	sweep.checkFailures["gone"] = 2

	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	_, staleSeen := sweep.seenClaude["gone"]
	assert.False(t, staleSeen, "observation for a vanished agent must be pruned")
	_, staleFailures := sweep.checkFailures["gone"]
	assert.False(t, staleFailures, "failure streak for a vanished agent must be pruned")
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

	newZombieSweep().sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())

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

	newZombieSweep().sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())

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

	newZombieSweep().sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	assert.Equal(t, []string{"board-204-implementation"}, s.deleted)
}

// SC-263: a persistent post-suspend Docker/exec disruption makes IsProcessRunning
// error on every tick. Before the fix the agent is skipped forever.
func TestSweepZombieAgents_ReapsAfterPersistentCheckError(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "board-252-verification", ContainerID: "c1", CreatedAt: time.Now().Add(-5 * time.Minute)},
		},
		processUp:  map[string]bool{"c1": false},
		processErr: map[string]error{"c1": assertErr{}},
	}
	var reaped []string
	onReaped := func(name string) { reaped = append(reaped, name) }

	sweep := newZombieSweep()
	for range zombieMaxProcessCheckFailures {
		sweep.sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())
		assert.Empty(t, s.deleted, "must not reap before threshold")
	}
	sweep.sweepZombieAgents(context.Background(), s, onReaped, zerolog.Nop())
	assert.Equal(t, []string{"board-252-verification"}, s.deleted)
	assert.Equal(t, []string{"board-252-verification"}, reaped)
}

// A one-shot race (container briefly unreachable, then live) must NOT reap.
func TestSweepZombieAgents_ResetsCounterOnSuccess(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-1", ContainerID: "c1", CreatedAt: time.Now().Add(-5 * time.Minute)},
		},
		processUp:  map[string]bool{"c1": true},
		processErr: map[string]error{"c1": assertErr{}},
	}
	sweep := newZombieSweep()
	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
	assert.Empty(t, s.deleted)

	delete(s.processErr, "c1")
	for range zombieMaxProcessCheckFailures + 2 {
		sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
	}
	assert.Empty(t, s.deleted, "healthy process must never be reaped")
}

// Composition of SC-236 and SC-263: an idle-by-design agent that has never
// been observed running claude stays spared even when its liveness check
// fails persistently — the escalation presumes a dead *claude*, but an idle
// agent never promised one. The spare contract is absolute.
func TestSweepZombieAgents_SparesIdleUnderPersistentCheckError(t *testing.T) {
	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "auth", ContainerID: "c1", CreatedAt: time.Now().Add(-5 * time.Minute), Idle: true},
		},
		processUp:  map[string]bool{"c1": false},
		processErr: map[string]error{"c1": assertErr{}},
	}
	sweep := newZombieSweep()
	for range zombieMaxProcessCheckFailures + 3 {
		sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
	}
	assert.Empty(t, s.deleted, "idle agent never seen with claude must survive even a broken liveness check")
}

// SC-427: the sweep runs on a single goroutine. If one reap hangs (a stalled
// CopyTranscript inside DeleteAgent) the tick must still return under a hard
// deadline, so a later tick can reap a *different* dead agent. Before the
// per-reap isolation fix, tick 1 blocks forever and the 3s guard below fires.
func TestSweepZombieAgents_HungReapDoesNotStarveNextTick(t *testing.T) {
	stuckRelease := make(chan struct{})
	defer close(stuckRelease) // let the abandoned reap goroutine finish at test end

	s := &mockSweeper{
		agents: []AgentInfo{
			{Name: "agent-stuck", ContainerID: "c1", CreatedAt: time.Now().Add(-2 * time.Minute)},
		},
		processUp:   map[string]bool{"c1": false, "c2": false},
		blockDelete: map[string]chan struct{}{"agent-stuck": stuckRelease},
	}

	sweep := newZombieSweep()
	// Shrink the hard deadline so the test does not wait the production 45s.
	sweep.reapHardDeadline = 100 * time.Millisecond

	// Tick 1: the stuck agent's reap hangs; the tick must still RETURN.
	tick1 := make(chan struct{})
	go func() {
		sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
		close(tick1)
	}()
	select {
	case <-tick1:
	case <-time.After(3 * time.Second):
		t.Fatal("tick 1 was starved by a hung reap and never returned")
	}

	// Tick 2: a second, healthy-to-reap agent appears. It must be reaped even
	// though the first agent's reap is still parked.
	s.agents = append(s.agents, AgentInfo{Name: "agent-ok", ContainerID: "c2", CreatedAt: time.Now().Add(-2 * time.Minute)})
	tick2 := make(chan struct{})
	go func() {
		sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())
		close(tick2)
	}()
	select {
	case <-tick2:
	case <-time.After(3 * time.Second):
		t.Fatal("tick 2 was starved by the still-hung first reap")
	}

	assert.Contains(t, s.deletedNames(), "agent-ok", "next tick must reap a different dead agent")
}
