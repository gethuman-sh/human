package cmdstats

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
)

func TestRenderContainers_empty(t *testing.T) {
	var buf bytes.Buffer
	renderContainers(&buf, daemon.ContainerResourceReport{})
	assert.Contains(t, buf.String(), "capacity unknown")
	assert.Contains(t, buf.String(), "no container samples recorded")
}

func TestRenderContainers_rows(t *testing.T) {
	var buf bytes.Buffer
	renderContainers(&buf, daemon.ContainerResourceReport{
		Engine: daemon.ContainerEngineCapacity{Known: true, NCPU: 2, MemTotalBytes: 2 << 30},
		Stages: []stats.StageResources{
			{Stage: "implementation", Runs: 3, PeakMemBytes: 1900 << 20, MemLimitBytes: 2 << 30, AvgCPUPercent: 80, PeakCPUPercent: 190, OOMKills: 1},
		},
	})
	out := buf.String()
	assert.Contains(t, out, "engine: 2 CPUs, 2.0 GB memory")
	assert.Contains(t, out, "implementation")
	assert.Contains(t, out, "1.9 GB / 2.0 GB")
	assert.Contains(t, out, "190%")
}

func TestFormatBytes(t *testing.T) {
	assert.Equal(t, "512 B", formatBytes(512))
	assert.Equal(t, "1.5 KB", formatBytes(1536))
	assert.Equal(t, "62.7 GB", formatBytes(67323613184))
}

func TestContainersCmd_rejectsUnknownRange(t *testing.T) {
	cmd := buildContainersCmd()
	cmd.SetArgs([]string{"--range", "1y"})
	cmd.SetOut(&bytes.Buffer{})
	err := cmd.Execute()
	assert.Error(t, err)
}
