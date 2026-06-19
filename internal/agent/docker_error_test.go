package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	humanerrors "github.com/gethuman-sh/human/errors"
)

// dialConnectionError produces a genuine Docker SDK connection failure by
// pointing a client at an address that refuses connections and issuing a call.
// This exercises the same error type the agent start path encounters when the
// engine is unreachable, without depending on the SDK's deprecated
// ErrorConnectionFailed constructor.
func dialConnectionError(t *testing.T) error {
	t.Helper()
	// 127.0.0.1:1 is in the reserved low-port range with nothing listening, so
	// the dial fails fast with a connection error.
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	_, err = cli.ContainerList(context.Background(), container.ListOptions{})
	if err == nil {
		t.Fatal("expected a connection error dialing a closed port")
	}
	if !client.IsErrConnectionFailed(err) {
		t.Fatalf("expected a connection-failed error, got %v", err)
	}
	return err
}

// TestIsDockerUnreachableTraversesWrapChain asserts that a Docker SDK
// connection failure is recognized even after it has been wrapped by
// tozd/go/errors (as happens on the agent start path). Without chain traversal
// the actionable, context-aware error would never be selected and users would
// be left with the opaque generic message.
func TestIsDockerUnreachableTraversesWrapChain(t *testing.T) {
	connErr := dialConnectionError(t)
	if !isDockerUnreachable(connErr) {
		t.Fatal("bare connection error must be detected")
	}

	wrapped := humanerrors.WrapWithDetails(connErr, "starting agent container", "name", "demo")
	if !isDockerUnreachable(wrapped) {
		t.Fatal("wrapped connection error must still be detected through the chain")
	}
}

// TestIsDockerUnreachableIgnoresUnrelatedErrors guards against mislabeling a
// non-connection failure (e.g. a feature-install error) as an unreachable
// engine, which would hide the real cause behind a misleading remedy.
func TestIsDockerUnreachableIgnoresUnrelatedErrors(t *testing.T) {
	if isDockerUnreachable(errors.New("install.sh exited 1")) {
		t.Fatal("unrelated error must not be reported as a Docker connection failure")
	}
}
