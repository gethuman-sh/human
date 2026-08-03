package marker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A body that is not a [human:*] marker (a plain discussion comment) is never
// signed — provenance is for machine records, not free-form text.
func TestSign_nonMarkerPassthrough(t *testing.T) {
	body := "just a plain human comment, no marker header"
	assert.Equal(t, body, Sign(body, "m1", "rev1"))
}

// The signature lands as fields in the block, and ParseBody reads them back as
// queryable fields rather than body.
func TestSign_insertsFieldsIntoBlock(t *testing.T) {
	signed := Sign("[human:review-started]", "m1", "rev1")

	m, ok := ParseBody(signed)
	require.True(t, ok)
	assert.Equal(t, "m1", m.Fields[MachineField])
	assert.Equal(t, "rev1", m.Fields[BuildField])
	assert.Empty(t, m.Body)
}

// With a prose body, the signature is inserted BEFORE the body so it stays in
// the field block; the body is preserved intact.
func TestSign_withProseBodyFieldsLandBeforeBody(t *testing.T) {
	signed := Sign("[human:implementation-failed]\nthe build broke on step 3", "m1", "rev1")

	m, ok := ParseBody(signed)
	require.True(t, ok)
	assert.Equal(t, "m1", m.Fields[MachineField])
	assert.Equal(t, "rev1", m.Fields[BuildField])
	assert.Equal(t, "the build broke on step 3", m.Body)
}

// Signing an already-signed body is a no-op, so a re-post or a double-wrap
// during migration cannot double-stamp.
func TestSign_idempotent(t *testing.T) {
	once := Sign("[human:review-started]\nbranch: main", "m1", "rev1")
	twice := Sign(once, "OTHER", "OTHERBUILD")
	assert.Equal(t, once, twice)
}

// An empty machine and build add nothing — an un-provisioned daemon or an
// unstamped binary still posts a valid, unsigned marker.
func TestSign_emptyMachineAndBuildOmitted(t *testing.T) {
	body := "[human:review-started]\nbranch: main"
	assert.Equal(t, body, Sign(body, "", ""))
	assert.Equal(t, body, Sign(body, "  ", "  "))
}

// Only the present half of the signature is added when the other is empty.
func TestSign_partialSignature(t *testing.T) {
	signed := Sign("[human:review-started]", "m1", "")
	m, ok := ParseBody(signed)
	require.True(t, ok)
	assert.Equal(t, "m1", m.Fields[MachineField])
	_, hasBuild := m.Fields[BuildField]
	assert.False(t, hasBuild)
}

// Existing field order is preserved — the signature is appended after the last
// field, never reordering the handoff's engineering/branch/commits block.
func TestSign_preservesFieldOrder(t *testing.T) {
	signed := Sign("[human:ready-for-review]\nengineering: HUM-9\nbranch: main\ncommits: 2037e40", "m1", "rev1")

	assert.Equal(t,
		"[human:ready-for-review]\nengineering: HUM-9\nbranch: main\ncommits: 2037e40\nmachine: m1\nbuild: rev1",
		signed)
}

// recordingProvider records AddComment bodies and answers GetIssue with a
// sentinel so "other methods delegate verbatim" is observable. The embedded
// interface is nil: only the methods a test exercises are implemented.
type recordingProvider struct {
	tracker.Provider
	added       []string
	getterCalls int
}

func (r *recordingProvider) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	r.added = append(r.added, body)
	return &tracker.Comment{Body: body}, nil
}

func (r *recordingProvider) GetIssue(_ context.Context, key string) (*tracker.Issue, error) {
	r.getterCalls++
	return &tracker.Issue{Key: key}, nil
}

func TestSigningProvider_signsAddCommentDelegatesRest(t *testing.T) {
	inner := &recordingProvider{}
	p := NewSigningProvider(inner, "m1", "rev1")

	_, err := p.AddComment(context.Background(), "SC-1", "[human:review-started]")
	require.NoError(t, err)
	require.Len(t, inner.added, 1)
	assert.Equal(t, "m1", ParseFieldForTest(t, inner.added[0], MachineField))

	// A non-comment method flows straight through to the inner provider.
	issue, err := p.GetIssue(context.Background(), "SC-1")
	require.NoError(t, err)
	assert.Equal(t, "SC-1", issue.Key)
	assert.Equal(t, 1, inner.getterCalls)
}

// recordingCommenter is the Commenter-narrowed double for SigningCommenter.
type recordingCommenter struct {
	added     []string
	listCalls int
}

func (r *recordingCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	r.listCalls++
	return nil, nil
}

func (r *recordingCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	r.added = append(r.added, body)
	return &tracker.Comment{Body: body}, nil
}

func TestSigningCommenter_signsAddCommentDelegatesList(t *testing.T) {
	inner := &recordingCommenter{}
	c := NewSigningCommenter(inner, "m1", "rev1")

	_, err := c.AddComment(context.Background(), "SC-1", "[human:review-started]")
	require.NoError(t, err)
	require.Len(t, inner.added, 1)
	assert.Equal(t, "m1", ParseFieldForTest(t, inner.added[0], MachineField))

	_, err = c.ListComments(context.Background(), "SC-1")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.listCalls)
}

// ParseFieldForTest reads a single field off a marker body for assertions.
func ParseFieldForTest(t *testing.T, body, field string) string {
	t.Helper()
	m, ok := ParseBody(body)
	require.True(t, ok)
	return m.Fields[field]
}
