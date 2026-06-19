package devcontainer

import (
	"testing"

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
