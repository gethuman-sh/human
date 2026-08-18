package ideadraft

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// comment renders a provenance record the way a writer would, at a distinct
// time — marker.Latest breaks a tie toward the earlier comment, so fixtures
// never share a Created second.
func comment(m marker.Marker, at time.Time) tracker.Comment {
	return tracker.Comment{Body: marker.Render(m, FieldOrder), Created: at}
}

func TestFingerprint_ExactBytes(t *testing.T) {
	a := Fingerprint("a")
	b := Fingerprint("a ")
	assert.NotEqual(t, a, b, "trailing whitespace is a different description")
	assert.Contains(t, a, "sha256:")
	assert.Contains(t, b, "sha256:")
}

func TestTBACount(t *testing.T) {
	assert.Equal(t, 2, TBACount("x [TBA: who?] y [TBA: when?]"))
	assert.Equal(t, 0, TBACount("none here"))
}

func TestDecide_NoMarkerEmptyDescription(t *testing.T) {
	v, _ := Decide(true, "an idea", "", nil)
	assert.Equal(t, VerdictWrite, v)
}

func TestDecide_NoMarkerNonEmptyDescription(t *testing.T) {
	v, reason := Decide(true, "an idea", "hand written", nil)
	assert.Equal(t, VerdictStandDown, v)
	assert.Contains(t, reason, "unknown provenance")
}

func TestDecide_MachineWroteItAndTitleChanged(t *testing.T) {
	drafted := "the draft"
	rec := MachineRecord(drafted, "old")
	v, _ := Decide(true, "new", drafted, []tracker.Comment{comment(rec, time.Unix(100, 0))})
	assert.Equal(t, VerdictWrite, v)
}

// The redraft loop break: the drafter's own write bumps UpdatedAt, so a run
// whose input has not changed must do nothing at all.
func TestDecide_MachineWroteItAndNothingChanged(t *testing.T) {
	drafted := "the draft"
	rec := MachineRecord(drafted, "old")
	v, _ := Decide(true, "old", drafted, []tracker.Comment{comment(rec, time.Unix(100, 0))})
	assert.Equal(t, VerdictCurrent, v)
}

func TestDecide_HumanEditedSinceTheDraft(t *testing.T) {
	drafted := "the draft"
	rec := MachineRecord(drafted, "old")
	v, _ := Decide(true, "old", drafted+" edit", []tracker.Comment{comment(rec, time.Unix(100, 0))})
	assert.Equal(t, VerdictStandDown, v)
}

// An emptied description does not re-open the door: once a human owns the
// words, nothing automatic writes to this ticket again.
func TestDecide_HumanRecordIsFinal(t *testing.T) {
	v, _ := Decide(true, "t", "", []tracker.Comment{comment(HumanRecord("whatever"), time.Unix(100, 0))})
	assert.Equal(t, VerdictStandDown, v)
}

func TestDecide_NotAnIdea(t *testing.T) {
	v, reason := Decide(false, "t", "", nil)
	assert.Equal(t, VerdictStandDown, v)
	assert.Contains(t, reason, "not an idea")
}

func TestDecide_LatestMarkerWins(t *testing.T) {
	drafted := "the draft"
	comments := []tracker.Comment{
		comment(MachineRecord(drafted, "old"), time.Unix(100, 0)),
		comment(HumanRecord(drafted), time.Unix(200, 0)),
	}
	v, _ := Decide(true, "new", drafted, comments)
	assert.Equal(t, VerdictStandDown, v)
}

func TestLatestProvenance_AbsentAuthorReadsAsMachine(t *testing.T) {
	body := "[human:idea-draft]\ndescription: sha256:x\n"
	p := LatestProvenance([]tracker.Comment{{Body: body, Created: time.Unix(1, 0)}})
	assert.True(t, p.Found)
	assert.Equal(t, AuthorMachine, p.Author)
}
