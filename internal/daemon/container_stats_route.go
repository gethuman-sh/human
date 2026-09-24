package daemon

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/gethuman-sh/human/internal/containerres"
	"github.com/gethuman-sh/human/internal/stats"
)

// ContainerResourceReport is what `human stats containers` renders: the
// engine's ceiling beside the per-stage roll-up, so the reader compares the
// two without a second command.
type ContainerResourceReport struct {
	Engine ContainerEngineCapacity `json:"engine"`
	Stages []stats.StageResources  `json:"stages"`
}

// ContainerEngineCapacity is the engine's ceiling as the report carries it.
// Known is false when the engine could not be asked, which the renderer says
// rather than printing zero CPUs.
type ContainerEngineCapacity struct {
	Known         bool  `json:"known"`
	NCPU          int   `json:"ncpu"`
	MemTotalBytes int64 `json:"mem_total_bytes"`
}

// handleContainerStats returns the container resource report for the range.
// An unset stats store yields an empty report, the same degrade-to-empty
// contract the other read routes use.
func (s *Server) handleContainerStats(conn net.Conn, args []string) {
	report := ContainerResourceReport{Stages: []stats.StageResources{}}
	ctx := context.Background()
	if s.StatsStore != nil {
		now := time.Now().UTC()
		stages, err := s.StatsStore.QueryContainerResources(ctx, rangeSince(parseRangeArg(args), now), now)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		if stages != nil {
			report.Stages = stages
		}
	}
	report.Engine = engineCapacity(ctx, s.ResourceProber)
	data, err := json.Marshal(report)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	_ = json.NewEncoder(conn).Encode(Response{Stdout: string(data) + "\n"})
}

func engineCapacity(ctx context.Context, prober containerres.Prober) ContainerEngineCapacity {
	if prober == nil {
		return ContainerEngineCapacity{}
	}
	ctx, cancel := context.WithTimeout(ctx, containerProbeTimeout)
	defer cancel()
	capacity, err := prober.EngineCapacity(ctx)
	if err != nil {
		return ContainerEngineCapacity{}
	}
	return ContainerEngineCapacity{Known: true, NCPU: capacity.NCPU, MemTotalBytes: capacity.MemTotal}
}
