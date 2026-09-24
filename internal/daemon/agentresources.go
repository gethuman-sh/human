package daemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/containerres"
	"github.com/gethuman-sh/human/internal/stats"
)

// AgentResourceSampleInterval is how often every running agent container is
// asked what it costs. Each reading takes the engine a second (it needs two
// CPU readings for a rate), so this is per-container work the sweep's 5s tick
// would make continuous.
const AgentResourceSampleInterval = 30 * time.Second

// containerProbeTimeout bounds every individual engine round-trip made from
// the sampler loop and the exit recorder. ContainerStats deliberately blocks
// for at least a second (it takes two CPU readings for a rate), so the bound
// must clear that; an unreachable or hung engine must not be able to block a
// caller past this, since both callers run on loops that must keep advancing
// (the sampler's own ticker goroutine, and the zombie sweep's single reap
// goroutine — SC-5369, matching the reap hard deadline's SC-427 reasoning).
const containerProbeTimeout = 5 * time.Second

// AgentLister is the slice of the zombie sweeper the sampler needs: which
// agents are running and in which containers.
type AgentLister interface {
	RunningAgents() ([]AgentInfo, error)
}

// ContainerSampleSink is where readings go; the stats store satisfies it.
type ContainerSampleSink interface {
	InsertContainerSample(ctx context.Context, c stats.ContainerSample) error
}

// Exit reasons recorded on an exit row: the sweep found claude gone, or found
// it silent past its idle budget and reaped it (docs/reaper.md).
const (
	ExitReasonDied   = "died"
	ExitReasonSilent = "silent"
)

// RunAgentResourceSampler writes one run row per running agent container every
// interval until ctx ends. A nil lister, prober or sink disables it, following
// the daemon's nil-disables convention, so a build without a stats store loses
// the record and nothing else.
func RunAgentResourceSampler(ctx context.Context, lister AgentLister, prober containerres.Prober, sink ContainerSampleSink, interval time.Duration, logger zerolog.Logger) {
	if lister == nil || prober == nil || sink == nil {
		return
	}
	logger.Info().Dur("interval", interval).Msg("agent resource sampler started")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			SampleRunningAgents(ctx, lister, prober, sink, time.Now().UTC(), logger)
		}
	}
}

// SampleRunningAgents takes one reading of every running agent container. A
// container that cannot be read (it may have exited between list and read) is
// skipped for this tick; the next tick asks again.
func SampleRunningAgents(ctx context.Context, lister AgentLister, prober containerres.Prober, sink ContainerSampleSink, now time.Time, logger zerolog.Logger) {
	agents, err := lister.RunningAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("resource sampler: failed to list agents")
		return
	}
	for _, a := range agents {
		if a.ContainerID == "" {
			continue
		}
		reading, err := probeStats(ctx, prober, a.ContainerID)
		if err != nil {
			logger.Debug().Err(err).Str("agent", a.Name).Msg("resource sampler: container stats unavailable")
			continue
		}
		row := sampleRow(a, reading, now)
		if err := sink.InsertContainerSample(ctx, row); err != nil {
			logger.Warn().Err(err).Str("agent", a.Name).Msg("resource sampler: could not record sample")
		}
	}
}

// AgentExitRecorder takes the last reading of a container the sweep is about
// to remove. It is what turns "the agent died" into "the agent died at 1.9 GB
// of 2 GB, and the engine says it was killed for memory".
type AgentExitRecorder struct {
	Prober containerres.Prober
	Sink   ContainerSampleSink
	Logger zerolog.Logger
}

// RecordExit writes the exit row. The container must still exist: the sweep
// calls this before DeleteAgent. A failed reading still records the exit with
// what could be learned, so a reap never goes unrecorded because stats were
// briefly unavailable.
func (r *AgentExitRecorder) RecordExit(ctx context.Context, a AgentInfo, reason ReapReason) {
	if r == nil || r.Sink == nil || a.ContainerID == "" {
		return
	}
	row := stats.ContainerSample{
		Timestamp: time.Now().UTC(), Project: a.ProjectDir, Agent: a.Name, ContainerID: a.ContainerID,
		Phase: stats.PhaseExit, Reason: exitReason(reason),
	}
	row.Key, row.Stage = keyAndStage(a.Name)
	if r.Prober != nil {
		// Bounded: this runs synchronously in front of the zombie sweep's reap
		// hard deadline (SC-427), so a hung engine call here must not be able to
		// stall the sweep goroutine indefinitely (SC-5369).
		if reading, err := probeStats(ctx, r.Prober, a.ContainerID); err == nil {
			applyReading(&row, reading)
		}
		if exit, err := probeExitState(ctx, r.Prober, a.ContainerID); err == nil {
			code := exit.ExitCode
			row.ExitCode = &code
			row.OOMKilled = exit.OOMKilled
		}
	}
	if row.OOMKilled {
		// Loud on purpose: without this line an OOM reads in the daemon log
		// exactly like an agent that crashed on its own.
		r.Logger.Warn().Str("agent", a.Name).Uint64("mem_usage", row.MemUsage).Uint64("mem_limit", row.MemLimit).
			Msg("container killed for memory: the engine ran out, not the agent")
	}
	if err := r.Sink.InsertContainerSample(ctx, row); err != nil {
		r.Logger.Warn().Err(err).Str("agent", a.Name).Msg("resource sampler: could not record exit")
	}
}

// probeStats and probeExitState bound a single engine round-trip to
// containerProbeTimeout so an unreachable or hung engine cannot block the
// caller's loop past that, regardless of how long the caller's own ctx lives.
func probeStats(ctx context.Context, prober containerres.Prober, containerID string) (containerres.Sample, error) {
	probeCtx, cancel := context.WithTimeout(ctx, containerProbeTimeout)
	defer cancel()
	return prober.ContainerStats(probeCtx, containerID)
}

func probeExitState(ctx context.Context, prober containerres.Prober, containerID string) (containerres.ExitState, error) {
	probeCtx, cancel := context.WithTimeout(ctx, containerProbeTimeout)
	defer cancel()
	return prober.ContainerExitState(probeCtx, containerID)
}

func exitReason(reason ReapReason) string {
	if reason.Silent {
		return ExitReasonSilent
	}
	return ExitReasonDied
}

func sampleRow(a AgentInfo, reading containerres.Sample, now time.Time) stats.ContainerSample {
	row := stats.ContainerSample{
		Timestamp: now, Project: a.ProjectDir, Agent: a.Name, ContainerID: a.ContainerID, Phase: stats.PhaseRun,
	}
	row.Key, row.Stage = keyAndStage(a.Name)
	applyReading(&row, reading)
	return row
}

func applyReading(row *stats.ContainerSample, reading containerres.Sample) {
	row.MemUsage = reading.MemUsage
	row.MemLimit = reading.MemLimit
	row.CPUPercent = reading.CPUPercent
	row.OnlineCPUs = reading.OnlineCPUs
	row.PIDs = reading.PIDs
}

// keyAndStage attributes a board agent to its ticket and stage; an agent that
// is not a board run keeps the empty attribution rather than a guessed one.
func keyAndStage(agentName string) (string, string) {
	key, stage, ok := parseAgentName(agentName)
	if !ok {
		return "", ""
	}
	return key, string(stage)
}
