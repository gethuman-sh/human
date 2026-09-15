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
	assert.Equal(t, ReasonUnknownProvenance, reason)
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
	assert.Equal(t, ReasonNotAnIdea, reason)
}

// The two stand-downs are not interchangeable: one says a person owns these
// words, the other says this ticket is not an idea right now. Recording the
// second as the first would freeze a re-labelled ticket's description for good.
func TestPinsHuman_OnlyDescriptionStandDowns(t *testing.T) {
	assert.True(t, PinsHuman(VerdictStandDown, ReasonUnknownProvenance))
	assert.True(t, PinsHuman(VerdictStandDown, ReasonChangedSinceDraft))
	assert.True(t, PinsHuman(VerdictStandDown, ReasonHumanAuthored))
	assert.False(t, PinsHuman(VerdictStandDown, ReasonNotAnIdea))
	assert.False(t, PinsHuman(VerdictWrite, ReasonNoPriorDraft))
	assert.False(t, PinsHuman(VerdictCurrent, ReasonAlreadyCurrent))
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

// verdictCase is one row of the guard's whole reason table: the same inputs
// asked twice, once as a background run and once as a user-initiated recreate.
type verdictCase struct {
	name        string
	isIdea      bool
	title       string
	description string
	comments    []tracker.Comment
	wantVerdict Verdict
	wantReason  Reason
}

func verdictCases() []verdictCase {
	drafted := "the draft"
	machine := []tracker.Comment{comment(MachineRecord(drafted, "old"), time.Unix(100, 0))}
	human := []tracker.Comment{comment(HumanRecord("a person's words"), time.Unix(100, 0))}
	return []verdictCase{
		{"not an idea", false, "t", "anything", nil, VerdictStandDown, ReasonNotAnIdea},
		{"human authored", true, "t", "a person's words", human, VerdictStandDown, ReasonHumanAuthored},
		{"unknown provenance", true, "t", "hand written", nil, VerdictStandDown, ReasonUnknownProvenance},
		{"changed since draft", true, "old", "edited by hand", machine, VerdictStandDown, ReasonChangedSinceDraft},
		{"already current", true, "old", drafted, machine, VerdictCurrent, ReasonAlreadyCurrent},
		{"no prior draft", true, "t", "", nil, VerdictWrite, ReasonNoPriorDraft},
		{"source changed", true, "new", drafted, machine, VerdictWrite, ReasonSourceChanged},
	}
}

// The whole point of the recreate flag: every reason the guard has to refuse is
// outranked by the user asking. ReasonAlreadyCurrent is the regression that
// matters — a card drafted in Ideas and promoted with its title unchanged lands
// exactly there, so lifting only the isIdea gate would leave the button dead.
func TestVerdictFor_RecreateOutranksEveryRefusal(t *testing.T) {
	for _, tc := range verdictCases() {
		t.Run(tc.name, func(t *testing.T) {
			v, reason := VerdictFor(true, tc.isIdea, tc.title, tc.description, tc.comments)
			assert.Equal(t, VerdictWrite, v)
			assert.Equal(t, ReasonRecreateRequested, reason)
		})
	}
}

// Without the flag, VerdictFor is Decide verbatim — the proof that background
// behaviour did not move.
func TestVerdictFor_WithoutRecreateIsDecide(t *testing.T) {
	for _, tc := range verdictCases() {
		t.Run(tc.name, func(t *testing.T) {
			v, reason := VerdictFor(false, tc.isIdea, tc.title, tc.description, tc.comments)
			assert.Equal(t, tc.wantVerdict, v)
			assert.Equal(t, tc.wantReason, reason)

			wantV, wantR := Decide(tc.isIdea, tc.title, tc.description, tc.comments)
			assert.Equal(t, wantV, v)
			assert.Equal(t, wantR, reason)
		})
	}
}

// A recreate never produces a stand-down, so it can never pin the description
// as human-authored on its way past the guard.
func TestPinsHuman_NeverPinsARecreate(t *testing.T) {
	v, reason := VerdictFor(true, false, "t", "a person's words", nil)
	assert.False(t, PinsHuman(v, reason))
}
