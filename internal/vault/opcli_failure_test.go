package vault

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

const testRef = "1pw://Private/Shortcut Token/notesPlain"

// TestOpCLIHelperProcess is not a real test: it is re-executed as a child so
// the suite can obtain a GENUINE *exec.ExitError (with a real ProcessState and
// captured stderr) instead of a hand-built one, which cannot report an exit
// code. The standard library uses the same idiom for exactly this reason.
func TestOpCLIHelperProcess(t *testing.T) {
	if os.Getenv("HUMAN_OPCLI_HELPER") != "1" {
		return
	}
	if diag := os.Getenv("HUMAN_OPCLI_STDERR"); diag != "" {
		_, _ = os.Stderr.WriteString(diag)
	}
	os.Exit(3)
}

// exitErrorWithStderr runs the helper process and returns the *exec.ExitError
// its failure produced.
func exitErrorWithStderr(t *testing.T, diag string) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestOpCLIHelperProcess") // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), "HUMAN_OPCLI_HELPER=1", "HUMAN_OPCLI_STDERR="+diag)
	_, err := cmd.Output()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "the helper must fail with an ExitError")
	return exitErr
}

// resolveWith runs Resolve against a stubbed op invocation.
func resolveWith(t *testing.T, run func(ctx context.Context) ([]byte, error)) error {
	t.Helper()
	o := &OpCLI{
		Binary: "op",
		runner: func(ctx context.Context, _ string, _ ...string) ([]byte, error) { return run(ctx) },
	}
	_, err := o.Resolve(testRef)
	require.Error(t, err)
	return err
}

// The message is all the board banner ever shows, so it must name WHICH secret
// failed — with several references configured the old message could not even
// distinguish the Shortcut token from the GitHub one (SC-2005).
func TestOpFailure_MessageNamesTheReference(t *testing.T) {
	err := resolveWith(t, func(context.Context) ([]byte, error) {
		return nil, stderrors.New("boom")
	})

	assert.Contains(t, err.Error(), testRef, "the failing reference must be in the message itself")
	assert.Contains(t, err.Error(), "boom", "the underlying cause must be in the message itself")
}

// op's own stderr is the line that says what to do about it, so it belongs in
// the message rather than only in the attached details.
func TestOpFailure_SurfacesOpsDiagnostic(t *testing.T) {
	err := resolveWith(t, func(context.Context) ([]byte, error) {
		return nil, exitErrorWithStderr(t, "  [ERROR] not signed in\n")
	})

	assert.Contains(t, err.Error(), "not signed in")
	details := errors.AllDetails(err)
	assert.Equal(t, "[ERROR] not signed in", details["stderr"], "stderr is also attached for the log")
	assert.Equal(t, testRef, details["ref"])
	assert.True(t, IsAuthFailure(err), "a signed-out op stderr is a not-authenticated failure (SC-2042)")
}

// An item-not-found stderr is a missing-secret failure, distinct from a
// signed-out session — callers must be able to tell them apart (SC-2042).
func TestOpFailure_ItemMissing(t *testing.T) {
	err := resolveWith(t, func(context.Context) ([]byte, error) {
		return nil, exitErrorWithStderr(t, `[ERROR] "Shortcut Token" isn't an item`)
	})

	assert.True(t, IsSecretMissing(err))
	assert.Contains(t, err.Error(), testRef)
}

// A missing binary is a permanent misconfiguration, not a blip, and its remedy
// is different — the message must say so instead of reading like a failed read.
func TestOpFailure_MissingBinaryIsNamedAsSuch(t *testing.T) {
	err := resolveWith(t, func(context.Context) ([]byte, error) {
		return nil, exec.ErrNotFound
	})

	assert.Contains(t, err.Error(), "not found on PATH")
	assert.Contains(t, err.Error(), testRef)
	assert.True(t, IsStoreUnreachable(err), "an absent binary is a store-unreachable failure (SC-2042)")
}

// An exit status with nothing on stderr must still say something better than a
// bare exit code.
func TestOpFailure_EmptyStderrStillExplains(t *testing.T) {
	err := opFailure("op", testRef, exitErrorWithStderr(t, ""), nil)

	assert.Contains(t, err.Error(), "no diagnostic on stderr")
	assert.Contains(t, err.Error(), testRef)
}

// The timeout branch, exercised directly: a non-nil context error means the
// invocation was cut short rather than answered.
func TestOpFailure_ContextErrorReportsTimeout(t *testing.T) {
	err := opFailure("op", testRef, context.DeadlineExceeded, context.DeadlineExceeded)

	assert.Contains(t, err.Error(), "timed out")
	assert.Contains(t, err.Error(), testRef)
	assert.Contains(t, err.Error(), opTimeout.String())
	assert.True(t, IsStoreUnreachable(err), "a timeout is a store-unreachable failure (SC-2042)")
}
