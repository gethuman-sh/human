package cmddaemon

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

type fakeInspector struct {
	resp devcontainer.ContainerInspectResponse
	err  error
}

func (f fakeInspector) ContainerInspect(context.Context, string) (devcontainer.ContainerInspectResponse, error) {
	return f.resp, f.err
}

func TestRegisterAgentIP_MapsIPToAgent(t *testing.T) {
	reg := daemon.NewAgentIPRegistry()
	l := dockerAgentLauncher{agentIPs: reg}
	insp := fakeInspector{resp: devcontainer.ContainerInspectResponse{IPAddress: "172.17.0.9"}}

	l.registerAgentIP(context.Background(), insp, "cid", "board-SC-2555-implementation")

	ticket, stage, ok := reg.Attribute("172.17.0.9:5")
	assert.True(t, ok)
	assert.Equal(t, "SC-2555", ticket)
	assert.Equal(t, "implementation", stage)
}

func TestRegisterAgentIP_NilRegistryNoOp(t *testing.T) {
	l := dockerAgentLauncher{}
	assert.NotPanics(t, func() {
		l.registerAgentIP(context.Background(), fakeInspector{}, "cid", "board-x-y")
	})
}

func TestRegisterAgentIP_InspectErrorLeavesUnattributed(t *testing.T) {
	reg := daemon.NewAgentIPRegistry()
	l := dockerAgentLauncher{agentIPs: reg}
	insp := fakeInspector{err: errors.New("inspect failed")}

	l.registerAgentIP(context.Background(), insp, "cid", "board-SC-1-implementation")

	_, _, ok := reg.Attribute("172.17.0.9:5")
	assert.False(t, ok, "a failed inspect never plants a mapping")
}

func TestRegisterAgentIP_EmptyContainerIDNoOp(t *testing.T) {
	reg := daemon.NewAgentIPRegistry()
	l := dockerAgentLauncher{agentIPs: reg}
	// An empty container ID means there is nothing to inspect: no mapping is planted.
	l.registerAgentIP(context.Background(), fakeInspector{resp: devcontainer.ContainerInspectResponse{IPAddress: "172.17.0.9"}}, "", "board-SC-1-implementation")
	_, _, ok := reg.Attribute("172.17.0.9:5")
	assert.False(t, ok)
}
