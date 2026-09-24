// Package containerres names what an agent container costs the engine and what
// the engine has to give. It is a leaf: the engine client implements it and the
// daemon consumes it, and neither has to import the other for it (SC-5369).
package containerres

import "context"

// Sample is one reading of what a container costs. MemLimit is the ceiling the
// engine enforces on this container: with no explicit cap it is the engine's
// own memory, which on a Mac is the VM's, so "usage against limit" reads the
// same whether or not a cap was set.
type Sample struct {
	MemUsage   uint64
	MemLimit   uint64
	CPUPercent float64
	OnlineCPUs int
	PIDs       uint64
}

// Capacity is what the engine has to give: on Linux the host, on a Mac the VM
// Docker Desktop, Colima or OrbStack runs containers in. It is the number a
// user has to compare their agents' peak usage against.
type Capacity struct {
	NCPU     int
	MemTotal int64
}

// ExitState is a container's final word: its exit code and whether the engine
// killed it for memory. It exists only while the container does.
type ExitState struct {
	ExitCode  int
	OOMKilled bool
}

// Prober reads container resource usage, exit state and the engine's capacity.
type Prober interface {
	ContainerStats(ctx context.Context, containerID string) (Sample, error)
	ContainerExitState(ctx context.Context, containerID string) (ExitState, error)
	EngineCapacity(ctx context.Context) (Capacity, error)
}
