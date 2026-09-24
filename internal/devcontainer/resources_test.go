package devcontainer

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
)

func TestSampleFrom_subtractsPageCacheAndScalesCPU(t *testing.T) {
	stats := container.StatsResponse{
		MemoryStats: container.MemoryStats{
			Usage: 1000, Limit: 4000,
			Stats: map[string]uint64{"inactive_file": 200},
		},
		CPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 300},
			SystemUsage: 1000,
			OnlineCPUs:  4,
		},
		PreCPUStats: container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: 100},
			SystemUsage: 600,
		},
		PidsStats: container.PidsStats{Current: 7},
	}
	s := sampleFrom(stats)
	assert.Equal(t, uint64(800), s.MemUsage)
	assert.Equal(t, uint64(4000), s.MemLimit)
	// 200 of a 400 system delta on 4 CPUs is two CPUs busy.
	assert.InDelta(t, 200.0, s.CPUPercent, 0.001)
	assert.Equal(t, 4, s.OnlineCPUs)
	assert.Equal(t, uint64(7), s.PIDs)
}

func TestSampleFrom_cgroupV1CacheKeyAndNoPreviousSample(t *testing.T) {
	stats := container.StatsResponse{
		MemoryStats: container.MemoryStats{Usage: 1000, Stats: map[string]uint64{"total_inactive_file": 100}},
		CPUStats:    container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 300}, SystemUsage: 1000},
	}
	s := sampleFrom(stats)
	assert.Equal(t, uint64(900), s.MemUsage)
	// Without a previous sample there is no delta, and a lifetime average
	// would misstate the rate; zero says "not measured" rather than guessing.
	assert.Equal(t, 0.0, s.CPUPercent)
}

func TestSampleFrom_cacheLargerThanUsageIsNotSubtracted(t *testing.T) {
	stats := container.StatsResponse{
		MemoryStats: container.MemoryStats{Usage: 100, Stats: map[string]uint64{"inactive_file": 500}},
	}
	assert.Equal(t, uint64(100), sampleFrom(stats).MemUsage)
}
