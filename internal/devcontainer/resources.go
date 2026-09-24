package devcontainer

import (
	"context"
	"encoding/json"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/containerres"
)

// The engine client satisfies containerres.Prober. It is deliberately not part
// of DockerClient: that interface has half a dozen test fakes, and the daemon's
// sampler is the only consumer of this capability.

// ContainerStats takes one sample. IncludePreviousSample makes the engine take
// two readings a second apart so the CPU figure is a real rate rather than a
// lifetime average diluted by the container's idle start.
func (e *engineClient) ContainerStats(ctx context.Context, containerID string) (containerres.Sample, error) {
	res, err := e.cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return containerres.Sample{}, errors.WrapWithDetails(err, "reading container stats", "container", containerID)
	}
	defer func() { _ = res.Body.Close() }()
	var stats container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		return containerres.Sample{}, errors.WrapWithDetails(err, "decoding container stats", "container", containerID)
	}
	return sampleFrom(stats), nil
}

// ContainerExitState reads the container's final state; the OOM verdict is the
// engine's own and only ever set where a cgroup limit exists.
func (e *engineClient) ContainerExitState(ctx context.Context, containerID string) (containerres.ExitState, error) {
	inspect, err := e.ContainerInspect(ctx, containerID)
	if err != nil {
		return containerres.ExitState{}, errors.WrapWithDetails(err, "inspecting container for its exit state", "container", containerID)
	}
	return containerres.ExitState{ExitCode: inspect.State.ExitCode, OOMKilled: inspect.State.OOMKilled}, nil
}

// EngineCapacity asks the engine what it runs on.
func (e *engineClient) EngineCapacity(ctx context.Context) (containerres.Capacity, error) {
	res, err := e.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return containerres.Capacity{}, errors.WrapWithDetails(err, "reading engine info")
	}
	return containerres.Capacity{NCPU: res.Info.NCPU, MemTotal: res.Info.MemTotal}, nil
}

// sampleFrom reduces the engine's stats response to the figures the record
// keeps, the way the docker CLI computes them: page cache is not memory the
// workload holds, so it comes off the usage; the CPU rate is the container's
// share of the system's delta scaled to the CPUs it may run on.
func sampleFrom(stats container.StatsResponse) containerres.Sample {
	return containerres.Sample{
		MemUsage:   memoryInUse(stats.MemoryStats),
		MemLimit:   stats.MemoryStats.Limit,
		CPUPercent: cpuPercent(stats),
		OnlineCPUs: int(stats.CPUStats.OnlineCPUs),
		PIDs:       stats.PidsStats.Current,
	}
}

func memoryInUse(m container.MemoryStats) uint64 {
	// cgroup v2 reports the cache as inactive_file; cgroup v1 as total_inactive_file.
	// Whichever is present is what the docker CLI subtracts.
	if cache, ok := m.Stats["inactive_file"]; ok && cache < m.Usage {
		return m.Usage - cache
	}
	if cache, ok := m.Stats["total_inactive_file"]; ok && cache < m.Usage {
		return m.Usage - cache
	}
	return m.Usage
}

func cpuPercent(stats container.StatsResponse) float64 {
	// No previous sample means the delta would be the container's lifetime
	// total, an average diluted by its idle start, not a rate.
	if stats.PreCPUStats.SystemUsage == 0 {
		return 0
	}
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	cpus := float64(stats.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return cpuDelta / sysDelta * cpus * 100
}

// Verify interface compliance.
var _ containerres.Prober = (*engineClient)(nil)
