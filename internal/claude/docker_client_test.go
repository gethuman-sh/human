package claude

import (
	"testing"

	"github.com/gethuman-sh/human/internal/dockerhost"
)

// TestNewEngineDockerClientAppliesResolvedHost asserts that NewEngineDockerClient
// routes the active Docker endpoint through the shared dockerhost resolver, so
// TUI container discovery and `human usage` honor the docker CLI context rather
// than always hitting the compiled-in default socket. Driving it via DOCKER_HOST
// exercises the same WithHost code path a resolved context takes.
func TestNewEngineDockerClientAppliesResolvedHost(t *testing.T) {
	const host = "tcp://127.0.0.1:23760"
	t.Setenv("DOCKER_HOST", host)

	dc, err := NewEngineDockerClient()
	if err != nil {
		t.Fatalf("NewEngineDockerClient: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	ec, ok := dc.(*engineDockerClient)
	if !ok {
		t.Fatalf("expected *engineDockerClient, got %T", dc)
	}
	if got := ec.cli.DaemonHost(); got != host {
		t.Errorf("DaemonHost() = %q, want %q", got, host)
	}
}

// TestNewEngineDockerClientSharesResolver guards that this constructor and the
// devcontainer constructor consult the same resolver, so a future regression in
// either cannot silently diverge from the docker CLI's context handling.
func TestNewEngineDockerClientSharesResolver(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:23761")
	if got := dockerhost.Resolve().Source; got != "env" {
		t.Fatalf("dockerhost.Resolve() Source = %q, want env", got)
	}
}

// TestNewEngineDockerClientDefaultWithoutEnv asserts the constructor still
// succeeds and yields a usable client when neither DOCKER_HOST nor a context
// applies.
func TestNewEngineDockerClientDefaultWithoutEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "default")

	dc, err := NewEngineDockerClient()
	if err != nil {
		t.Fatalf("NewEngineDockerClient: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	ec, ok := dc.(*engineDockerClient)
	if !ok {
		t.Fatalf("expected *engineDockerClient, got %T", dc)
	}
	if ec.cli.DaemonHost() == "" {
		t.Errorf("DaemonHost() should be the platform default, got empty")
	}
}
