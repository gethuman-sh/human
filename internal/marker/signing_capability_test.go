package marker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// SigningProvider is the OUTERMOST decorator in the provider chain, and it
// embeds tracker.Provider — which promotes only the methods that interface
// declares. Any capability deliberately kept off Provider so backends without it
// need not fake it therefore vanishes at the last wrap, silently: nothing fails
// to compile, the type assertion just returns false, and the caller concludes
// the tracker cannot do something it can.
//
// That is not hypothetical. AssignToReporter was implemented on the client and
// relayed through the four inner wrappers, and calls still answered "tracker
// does not support assignment" — because this one, which nobody had counted,
// dropped it.
func TestSigningProviderPreservesOptionalCapabilities(t *testing.T) {
	inner := &capableProvider{}
	wrapped := marker.NewSigningProvider(inner, "machine-1", "build-1")

	assigner, ok := wrapped.(tracker.ReporterAssigner)
	require.True(t, ok, "the outermost wrapper drops ReporterAssigner — a caller sees a provider that cannot do what it can")

	require.NoError(t, assigner.AssignToReporter(context.Background(), "SC-1"))
	assert.Equal(t, []string{"SC-1"}, inner.reporterAssigned, "the call must reach the wrapped provider")
}

// A wrapped provider that genuinely lacks the capability reports it as absent
// rather than pretending, so the caller's fallback still works.
func TestSigningProviderReportsAGenuinelyAbsentCapability(t *testing.T) {
	wrapped := marker.NewSigningProvider(&plainProvider{}, "m", "b")

	assigner, ok := wrapped.(tracker.ReporterAssigner)
	require.True(t, ok, "the method exists on the wrapper either way")

	err := assigner.AssignToReporter(context.Background(), "SC-1")
	require.ErrorIs(t, err, tracker.ErrOwnershipUnsupported)
}

type plainProvider struct{ tracker.Provider }

type capableProvider struct {
	tracker.Provider
	reporterAssigned []string
}

func (c *capableProvider) AssignToReporter(_ context.Context, key string) error {
	c.reporterAssigned = append(c.reporterAssigned, key)
	return nil
}
