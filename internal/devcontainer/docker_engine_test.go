package devcontainer

import (
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/gethuman-sh/human/internal/dockerhost"
)

// TestNewDockerClientAppliesResolvedHost asserts that NewDockerClient routes
// the active Docker endpoint through the shared dockerhost resolver, so the
// devcontainer engine honors the docker CLI context (colima/OrbStack/etc.)
// instead of always hitting the compiled-in default socket. Driving it via
// DOCKER_HOST exercises the same WithHost code path a resolved context takes.
func TestNewDockerClientAppliesResolvedHost(t *testing.T) {
	const host = "tcp://127.0.0.1:23750"
	t.Setenv("DOCKER_HOST", host)

	dc, err := NewDockerClient()
	if err != nil {
		t.Fatalf("NewDockerClient: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	ec, ok := dc.(*engineClient)
	if !ok {
		t.Fatalf("expected *engineClient, got %T", dc)
	}
	if got := ec.cli.DaemonHost(); got != host {
		t.Errorf("DaemonHost() = %q, want %q", got, host)
	}
}

// TestNewDockerClientSharesResolver guards that this constructor and the claude
// constructor consult the same resolver: when DOCKER_HOST is set, the resolver
// must report Source "env" so neither constructor reinvents context handling.
func TestNewDockerClientSharesResolver(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:23751")
	if got := dockerhost.Resolve().Source; got != "env" {
		t.Fatalf("dockerhost.Resolve() Source = %q, want env", got)
	}
}

// TestNewDockerClientDefaultWithoutEnv asserts the constructor still succeeds
// and yields a usable client when neither DOCKER_HOST nor a context applies.
func TestNewDockerClientDefaultWithoutEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "default")

	dc, err := NewDockerClient()
	if err != nil {
		t.Fatalf("NewDockerClient: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	ec, ok := dc.(*engineClient)
	if !ok {
		t.Fatalf("expected *engineClient, got %T", dc)
	}
	if ec.cli.DaemonHost() == "" {
		t.Errorf("DaemonHost() should be the platform default, got empty")
	}
}

// TestContainerStateFrom_CarriesInitErrorAndNeverStarted pins the evidence a
// relaunch needs: a container that never ran keeps Docker's reason and a zero
// start time (SC-4632).
func TestContainerStateFrom_CarriesInitErrorAndNeverStarted(t *testing.T) {
	const initErr = "error during container init: error mounting \"/host/.git\" to rootfs: no such file or directory"
	got := containerStateFrom(&container.State{
		Status:    container.StateCreated,
		ExitCode:  127,
		Error:     initErr,
		StartedAt: "0001-01-01T00:00:00Z",
	})
	if got.Status != containerStateCreated {
		t.Errorf("Status = %q, want %q", got.Status, containerStateCreated)
	}
	if got.Error != initErr {
		t.Errorf("Error = %q, want %q", got.Error, initErr)
	}
	if got.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127", got.ExitCode)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero for a container that never ran", got.StartedAt)
	}
}

func TestContainerStateFrom_RunningCarriesStartTime(t *testing.T) {
	got := containerStateFrom(&container.State{
		Status:    container.StateRunning,
		Running:   true,
		StartedAt: "2026-08-26T10:11:12.123456789Z",
	})
	if !got.Running {
		t.Error("Running = false, want true")
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt is zero, want the parsed start time")
	}
}

func TestContainerStateFrom_NilAndUnparsableTime(t *testing.T) {
	if got := containerStateFrom(nil); got != (ContainerState{}) {
		t.Errorf("containerStateFrom(nil) = %+v, want zero value", got)
	}
	got := containerStateFrom(&container.State{Status: container.StateExited, StartedAt: "not-a-time"})
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero for an unreadable timestamp", got.StartedAt)
	}
}
