package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
)

// Every marker is signed before it is posted, and signing splices machine:/build:
// in as the first lines after the header. The card's reason must skip them: a
// positional read returns "machine: <id>", which is how every failed card on the
// board came to report its signature instead of what went wrong.
func TestFailureReasonSkipsTheSignatureFields(t *testing.T) {
	const diagnosis = "the PR reviewer stopped before recording a verdict — check the PR and its review, then re-run Deploy"
	body := marker.Sign(PRReviewFailedHeader+"\n"+diagnosis, "acf89556", "9c74fb5dc011")

	require.Contains(t, body, "machine: acf89556", "precondition: the marker really is signed")

	assert.Equal(t, diagnosis, failureReason(body))
	assert.Equal(t, diagnosis, failureBody(body))
}

// The multi-line surface keeps the whole diagnosis, still without the signature.
func TestFailureBodyKeepsTheWholeDiagnosisBelowTheSignature(t *testing.T) {
	body := marker.Sign(DeployFailedHeader+"\nheadline here\n\ndetail line one\ndetail line two", "4f3add9a", "")

	assert.Equal(t, "headline here\n\ndetail line one\ndetail line two", failureBody(body))
	assert.Equal(t, "headline here", failureReason(body))
}

// Threads written before signing existed carry no field block, and must still
// read exactly as they did.
func TestFailureReasonUnchangedForAnUnsignedMarker(t *testing.T) {
	body := DeployFailedHeader + "\nCI checks failed\npr: https://example.test/pr/1"

	assert.Equal(t, "CI checks failed", failureReason(body))
}

// A marker carrying nothing but a header and its signature has no reason to
// show; the card must still say something rather than render blank.
func TestFailureReasonFallsBackToTheHeaderWhenThereIsNoProse(t *testing.T) {
	body := marker.Sign(DeployFailedHeader, "4f3add9a", "abc123")

	assert.NotEmpty(t, failureReason(body))
	assert.NotContains(t, failureReason(body), "machine:", "a signature is never a diagnosis")
}
