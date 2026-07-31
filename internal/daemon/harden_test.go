package daemon

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The protection the daemon's credentials actually depend on: after hardening,
// the kernel refuses to write a core file for this process. Asserted through
// the kernel's own report rather than the return value, so a call that silently
// did nothing could not pass.
func TestHardenProcess_kernelStopsDumpingThisProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PR_SET_DUMPABLE is Linux-only")
	}

	HardenProcess(zerolog.Nop())

	status, err := os.ReadFile("/proc/self/status")
	require.NoError(t, err)
	assert.Contains(t, string(status), "CoreDumping:\t0",
		"a crash must not write a file containing every tracker credential")
}

// An operator who needs a debugger must have a way through, or the only route
// would be patching and rebuilding the daemon.
func TestHardenProcess_debugEscapeHatchIsHonoured(t *testing.T) {
	t.Setenv(AllowDebugEnv, "1")
	var logged strings.Builder
	logger := zerolog.New(&logged)

	HardenProcess(logger)

	assert.Contains(t, logged.String(), "hardening skipped",
		"skipping silently would leave the operator believing the daemon is hardened")
}

// Hardening is best-effort by design: it protects credentials from accidental
// disclosure, and refusing to start without it would trade that for an outage.
func TestHardenProcess_neverPanics(t *testing.T) {
	assert.NotPanics(t, func() { HardenProcess(zerolog.Nop()) })
}

// A dirty build is not the revision it names, and the provenance report exists
// precisely because /proc can no longer be consulted.
func TestBuildRevision_doesNotPanicAndIsStable(t *testing.T) {
	assert.NotPanics(t, func() { _ = BuildRevision() })
	assert.Equal(t, BuildRevision(), BuildRevision())
}
