package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/containerres"
	"github.com/gethuman-sh/human/internal/stats"
)

type fakeResourceLister struct {
	agents []AgentInfo
	err    error
}

func (f fakeResourceLister) RunningAgents() ([]AgentInfo, error) { return f.agents, f.err }

type fakeProber struct {
	samples  map[string]containerres.Sample
	exits    map[string]containerres.ExitState
	statsErr error
}

func (f fakeProber) ContainerStats(_ context.Context, id string) (containerres.Sample, error) {
	if f.statsErr != nil {
		return containerres.Sample{}, f.statsErr
	}
	s, ok := f.samples[id]
	if !ok {
		return containerres.Sample{}, errors.WithDetails("no such container", "id", id)
	}
	return s, nil
}

func (f fakeProber) ContainerExitState(_ context.Context, id string) (containerres.ExitState, error) {
	e, ok := f.exits[id]
	if !ok {
		return containerres.ExitState{}, errors.WithDetails("no such container", "id", id)
	}
	return e, nil
}

func (f fakeProber) EngineCapacity(context.Context) (containerres.Capacity, error) {
	return containerres.Capacity{NCPU: 2, MemTotal: 2 << 30}, nil
}

// hangingProber blocks every call on its ctx, standing in for an unreachable
// or hung engine. It exists to pin SC-5369: RecordExit and SampleRunningAgents
// must bound the ctx they hand the prober to containerProbeTimeout, or a
// caller using a long-lived ctx (context.Background(), the sweep's own
// long-lived root ctx) would block for as long as that ctx lives.
type hangingProber struct{}

func (hangingProber) ContainerStats(ctx context.Context, _ string) (containerres.Sample, error) {
	<-ctx.Done()
	return containerres.Sample{}, ctx.Err()
}

func (hangingProber) ContainerExitState(ctx context.Context, _ string) (containerres.ExitState, error) {
	<-ctx.Done()
	return containerres.ExitState{}, ctx.Err()
}

func (hangingProber) EngineCapacity(ctx context.Context) (containerres.Capacity, error) {
	<-ctx.Done()
	return containerres.Capacity{}, ctx.Err()
}

type recordingSink struct {
	mu   sync.Mutex
	rows []stats.ContainerSample
}

func (r *recordingSink) InsertContainerSample(_ context.Context, c stats.ContainerSample) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, c)
	return nil
}

func TestSampleRunningAgents_writesOneRunRowPerContainer(t *testing.T) {
	lister := fakeResourceLister{agents: []AgentInfo{
		{Name: "board-SC-1-implementation", ContainerID: "c1", ProjectDir: "/p"},
		{Name: "board-SC-2-planning", ContainerID: "c2", ProjectDir: "/p"},
		{Name: "idle", ContainerID: ""},
	}}
	prober := fakeProber{samples: map[string]containerres.Sample{
		"c1": {MemUsage: 100, MemLimit: 1000, CPUPercent: 50, OnlineCPUs: 2, PIDs: 3},
		"c2": {MemUsage: 200, MemLimit: 1000, CPUPercent: 10, OnlineCPUs: 2, PIDs: 4},
	}}
	sink := &recordingSink{}
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	SampleRunningAgents(context.Background(), lister, prober, sink, now, zerolog.Nop())

	require.Len(t, sink.rows, 2, "a container-less agent has nothing to sample")
	first := sink.rows[0]
	assert.Equal(t, stats.PhaseRun, first.Phase)
	assert.Equal(t, "SC-1", first.Key)
	assert.Equal(t, "implementation", first.Stage)
	assert.Equal(t, "/p", first.Project)
	assert.Equal(t, uint64(100), first.MemUsage)
	assert.Equal(t, uint64(1000), first.MemLimit)
	assert.Equal(t, 50.0, first.CPUPercent)
	assert.Equal(t, now, first.Timestamp)
	assert.Nil(t, first.ExitCode)
	assert.Equal(t, "planning", sink.rows[1].Stage)
}

func TestSampleRunningAgents_skipsAContainerThatCannotBeRead(t *testing.T) {
	lister := fakeResourceLister{agents: []AgentInfo{
		{Name: "board-SC-1-implementation", ContainerID: "gone"},
		{Name: "board-SC-2-planning", ContainerID: "c2"},
	}}
	prober := fakeProber{samples: map[string]containerres.Sample{"c2": {MemUsage: 1}}}
	sink := &recordingSink{}

	SampleRunningAgents(context.Background(), lister, prober, sink, time.Now(), zerolog.Nop())

	require.Len(t, sink.rows, 1)
	assert.Equal(t, "SC-2", sink.rows[0].Key)
}

func TestSampleRunningAgents_nonBoardAgentKeepsEmptyAttribution(t *testing.T) {
	lister := fakeResourceLister{agents: []AgentInfo{{Name: "scratch", ContainerID: "c1"}}}
	prober := fakeProber{samples: map[string]containerres.Sample{"c1": {MemUsage: 1}}}
	sink := &recordingSink{}

	SampleRunningAgents(context.Background(), lister, prober, sink, time.Now(), zerolog.Nop())

	require.Len(t, sink.rows, 1)
	assert.Empty(t, sink.rows[0].Key)
	assert.Empty(t, sink.rows[0].Stage)
	assert.Equal(t, "scratch", sink.rows[0].Agent)
}

func TestRecordExit_carriesExitCodeOOMAndReason(t *testing.T) {
	prober := fakeProber{
		samples: map[string]containerres.Sample{"c1": {MemUsage: 1900, MemLimit: 2000}},
		exits:   map[string]containerres.ExitState{"c1": {ExitCode: 137, OOMKilled: true}},
	}
	sink := &recordingSink{}
	rec := &AgentExitRecorder{Prober: prober, Sink: sink, Logger: zerolog.Nop()}

	rec.RecordExit(context.Background(), AgentInfo{Name: "board-SC-1-implementation", ContainerID: "c1"}, ReapReason{})

	require.Len(t, sink.rows, 1)
	row := sink.rows[0]
	assert.Equal(t, stats.PhaseExit, row.Phase)
	assert.Equal(t, ExitReasonDied, row.Reason)
	require.NotNil(t, row.ExitCode)
	assert.Equal(t, 137, *row.ExitCode)
	assert.True(t, row.OOMKilled)
	assert.Equal(t, uint64(1900), row.MemUsage)
	assert.Equal(t, "SC-1", row.Key)
}

func TestRecordExit_silenceReapIsRecordedAsSilent(t *testing.T) {
	prober := fakeProber{exits: map[string]containerres.ExitState{"c1": {ExitCode: 0}}}
	sink := &recordingSink{}
	rec := &AgentExitRecorder{Prober: prober, Sink: sink, Logger: zerolog.Nop()}

	rec.RecordExit(context.Background(), AgentInfo{Name: "board-SC-1-review", ContainerID: "c1"}, ReapReason{Silent: true, Idle: 4 * time.Minute})

	require.Len(t, sink.rows, 1)
	assert.Equal(t, ExitReasonSilent, sink.rows[0].Reason)
	require.NotNil(t, sink.rows[0].ExitCode)
	assert.Equal(t, 0, *sink.rows[0].ExitCode)
}

func TestRecordExit_unreadableContainerStillRecordsTheExit(t *testing.T) {
	prober := fakeProber{statsErr: errors.WithDetails("engine away")}
	sink := &recordingSink{}
	rec := &AgentExitRecorder{Prober: prober, Sink: sink, Logger: zerolog.Nop()}

	rec.RecordExit(context.Background(), AgentInfo{Name: "board-SC-1-review", ContainerID: "c1"}, ReapReason{})

	require.Len(t, sink.rows, 1)
	assert.Nil(t, sink.rows[0].ExitCode, "nothing was learned, nothing is invented")
	assert.Equal(t, uint64(0), sink.rows[0].MemUsage)
}

func TestRecordExit_boundsAHungEngineCall(t *testing.T) {
	sink := &recordingSink{}
	rec := &AgentExitRecorder{Prober: hangingProber{}, Sink: sink, Logger: zerolog.Nop()}

	done := make(chan struct{})
	go func() {
		// context.Background() never expires on its own: if RecordExit did not
		// bound the ctx it hands the prober, this call would hang for the life
		// of the process, exactly the starvation SC-427's hard deadline exists
		// to prevent one call upstream of it (SC-5369).
		rec.RecordExit(context.Background(), AgentInfo{Name: "board-SC-1-implementation", ContainerID: "c1"}, ReapReason{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * containerProbeTimeout):
		t.Fatal("RecordExit did not bound its engine calls: a hung prober blocked it past containerProbeTimeout")
	}
	require.Len(t, sink.rows, 1, "a hung engine still leaves a recorded exit, with nothing learned")
	assert.Nil(t, sink.rows[0].ExitCode)
}

func TestSampleRunningAgents_boundsAHungEngineCall(t *testing.T) {
	lister := fakeResourceLister{agents: []AgentInfo{{Name: "board-SC-1-implementation", ContainerID: "c1"}}}
	sink := &recordingSink{}

	done := make(chan struct{})
	go func() {
		SampleRunningAgents(context.Background(), lister, hangingProber{}, sink, time.Now(), zerolog.Nop())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * containerProbeTimeout):
		t.Fatal("SampleRunningAgents did not bound its engine call: a hung prober blocked the sampler tick past containerProbeTimeout")
	}
	assert.Empty(t, sink.rows, "an unreadable container is skipped for this tick")
}

func TestRecordExit_nilRecorderAndNilSinkAreNoOps(t *testing.T) {
	var nilRec *AgentExitRecorder
	nilRec.RecordExit(context.Background(), AgentInfo{Name: "a", ContainerID: "c1"}, ReapReason{})
	(&AgentExitRecorder{Prober: fakeProber{}}).RecordExit(context.Background(), AgentInfo{Name: "a", ContainerID: "c1"}, ReapReason{})
}

func TestZombieSweep_recordsTheExitBeforeDeletingTheAgent(t *testing.T) {
	s := &mockSweeper{
		agents:    []AgentInfo{{Name: "board-SC-9-implementation", ContainerID: "c9", CreatedAt: time.Now().Add(-time.Minute)}},
		processUp: map[string]bool{"c9": false},
	}
	sink := &recordingSink{}
	prober := fakeProber{exits: map[string]containerres.ExitState{"c9": {ExitCode: 1}}}
	sweep := newZombieSweep()
	sweep.exitRecorder = &AgentExitRecorder{Prober: prober, Sink: sink, Logger: zerolog.Nop()}

	sweep.sweepZombieAgents(context.Background(), s, nil, zerolog.Nop())

	require.Len(t, sink.rows, 1)
	assert.Equal(t, "SC-9", sink.rows[0].Key)
	assert.Equal(t, []string{"board-SC-9-implementation"}, s.deletedNames())
}

func TestRunAgentResourceSampler_nilDependenciesReturnAtOnce(t *testing.T) {
	done := make(chan struct{})
	go func() {
		RunAgentResourceSampler(context.Background(), nil, fakeProber{}, &recordingSink{}, time.Millisecond, zerolog.Nop())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sampler with a nil lister must return immediately")
	}
}
