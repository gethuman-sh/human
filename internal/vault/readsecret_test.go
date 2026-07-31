package vault

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The secret must come back intact — this replaced exec.Output, and a helper
// that loses trailing bytes or races the process exit would break every
// 1pw:// reference at once.
func TestReadSecretOutput_returnsTheWholeOutput(t *testing.T) {
	out, err := readSecretOutput(context.Background(), "printf", "the-token")

	require.NoError(t, err)
	assert.Equal(t, "the-token", string(out))
}

// A secret larger than a pipe buffer must not deadlock: reading before Wait is
// what makes that safe, and doing it the other way round would hang forever.
func TestReadSecretOutput_survivesOutputLargerThanAPipeBuffer(t *testing.T) {
	out, err := readSecretOutput(context.Background(), "sh", "-c", "yes token | head -c 200000")

	require.NoError(t, err)
	assert.Len(t, out, 200000)
}

// opFailure turns "exit status 1" into op's own diagnostic by reading Stderr
// off the ExitError. exec.Output populated that; this helper must too, or every
// credential error becomes opaque again.
func TestReadSecretOutput_carriesStderrOnTheExitError(t *testing.T) {
	_, err := readSecretOutput(context.Background(), "sh", "-c", "echo 'not signed in' >&2; exit 1")

	require.Error(t, err)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Contains(t, string(exitErr.Stderr), "not signed in",
		"without this every credential failure reads as a bare exit status")
}

// A hung helper must be killed by the context, not waited on forever — this is
// the 30-second timeout that surfaces as "the CLI is unresponsive or waiting on
// an unlock prompt".
func TestReadSecretOutput_contextCancellationStopsAHungHelper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := readSecretOutput(ctx, "sleep", "30")

	require.Error(t, err)
}

// A helper that does not exist is reported as such, so opFailure can tell
// "install it" apart from "it failed".
func TestReadSecretOutput_missingBinaryIsReportedAsNotFound(t *testing.T) {
	_, err := readSecretOutput(context.Background(), "human-no-such-credential-helper")

	require.ErrorIs(t, err, exec.ErrNotFound)
}
