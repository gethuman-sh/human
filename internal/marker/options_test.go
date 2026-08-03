package marker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A decision block is the pipeline's one sanctioned way to stop and ask a
// human. Posting one with a single answer parks a card on a question that has
// only one possible reply — which is what a stage does when it turns a
// condition it should have handled itself into a question.
func TestValidateRejectsSingleAnswerDecision(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":   "implementation",
			"context": "could not acquire the fix-stage lease",
			"1":       "Rebuild the branch",
		},
	}

	err := Validate(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least two answers")
}

// The daemon provenance stamp has the same shape as an answer, so counting it
// would let a one-answer block pass the floor.
func TestValidateDoesNotCountDaemonStampAsAnAnswer(t *testing.T) {
	m := Marker{
		Type:   "options",
		Fields: map[string]string{"stage": "implementation", "1": "Rebuild the branch"},
		Body:   "daemon: 4f3add9a",
	}

	require.Error(t, Validate(m), "stage, context and daemon are metadata, not answers")
}

// The machine/build signature fields have the same id: label shape as an
// answer; like the daemon stamp they are metadata and must not be counted, so a
// signed one-answer block still fails the floor.
func TestValidateDoesNotCountSignatureAsAnAnswer(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":      "implementation",
			"1":          "Rebuild the branch",
			MachineField: "alice-macbook",
			BuildField:   "abc123+dirty",
		},
	}

	require.Error(t, Validate(m), "machine and build are provenance metadata, not answers")
}

// A two-answer block signed with machine/build still validates: the signature is
// ignored and the two real answers clear the floor.
func TestValidateAcceptsSignedTwoAnswerDecision(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage":      "implementation",
			"1":          "Narrow the fix",
			"2":          "Fix the class",
			MachineField: "alice-macbook",
			BuildField:   "abc123+dirty",
		},
	}

	require.NoError(t, Validate(m))
}

func TestValidateAcceptsTwoAnswerDecision(t *testing.T) {
	m := Marker{
		Type: "options",
		Fields: map[string]string{
			"stage": "implementation",
			"1":     "Narrow the fix to the reported path",
			"2":     "Fix the class across all callers",
		},
	}

	require.NoError(t, Validate(m))
}

// Answers survive a round trip through the rendered comment body, where they
// arrive as body lines rather than fields — both shapes must count.
func TestValidateCountsAnswersFromBody(t *testing.T) {
	m := Marker{
		Type:   "options",
		Fields: map[string]string{"stage": "implementation"},
		Body:   "1: Narrow the fix\n2: Fix the class\ndaemon: 4f3add9a",
	}

	require.NoError(t, Validate(m))
}

// The stage requirement predates the answer floor and must still bite.
func TestValidateStillRequiresStage(t *testing.T) {
	m := Marker{
		Type:   "options",
		Fields: map[string]string{"1": "a", "2": "b"},
	}

	err := Validate(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing a required field")
}
