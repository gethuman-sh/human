package marker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A wait declared by an answer is metadata about that answer, not a third thing
// to pick. Counting it would let a block offering ONE real answer clear the
// two-answer floor — the same hole the daemon stamp opened, reopened by a line
// shaped like an answer.
func TestValidateDoesNotCountAWaitAsAnAnswer(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":         "implementation",
			"1":             "SC-4245 goes first",
			"waits-for-1":   "SC-4245",
			"waits-for-999": "SC-4245",
		},
	}

	require.Error(t, Validate(m), "a wait is metadata about an answer, never an answer")
}

// The ordinary sequencing block: two real answers, one of which declares what
// it waits for.
func TestValidateAcceptsASequencingBlock(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":       "implementation",
			"context":     "SC-4245 has an open branch on the same files",
			"1":           "SC-4245 goes first",
			"waits-for-1": "SC-4245",
			"2":           "this goes first",
		},
	}

	require.NoError(t, Validate(m))
}

// Read back off the ticket the answers arrive as body lines rather than posted
// fields, so the same rules have to hold on that side.
func TestValidateAcceptsASequencingBlockReadBack(t *testing.T) {
	m := Marker{
		Type:   "options",
		Fields: map[string]string{"stage": "implementation"},
		Body:   "1: SC-4245 goes first\nwaits-for-1: SC-4245\n2: this goes first",
	}

	require.NoError(t, Validate(m))
}

// A wait naming an answer the block does not offer holds nothing: the block
// renders as an ordinary fork and the work it was meant to defer starts anyway.
// Post time is the only moment anyone is watching.
func TestValidateRejectsAWaitForAnAnswerNotOffered(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":       "implementation",
			"1":           "SC-4245 goes first",
			"2":           "this goes first",
			"waits-for-3": "SC-4245",
		},
	}

	err := Validate(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not offer")
}
