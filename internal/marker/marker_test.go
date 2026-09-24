package marker

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestRender_headerFieldsBody(t *testing.T) {
	m := Marker{
		Type:   "ready-for-review",
		Fields: map[string]string{"branch": "main", "commits": "2037e40, 64bb370", "engineering": "HUM-89"},
	}
	out := Render(m, []string{"engineering", "branch", "commits"})
	assert.Equal(t, "[human:ready-for-review]\nengineering: HUM-89\nbranch: main\ncommits: 2037e40, 64bb370", out)
}

func TestRender_headToken(t *testing.T) {
	m := Marker{Type: "bug-verdict", Head: "confirmed", Body: "root cause: nil check"}
	out := Render(m, nil)
	assert.Equal(t, "[human:bug-verdict] confirmed\n\nroot cause: nil check", out)
}

func TestRender_multilineFieldIndentsContinuations(t *testing.T) {
	m := Marker{
		Type:   "review-complete",
		Fields: map[string]string{"verdict": "pass", "reviews": "HUM-89: pass — .human/reviews/hum-89.md\nHUM-90: pass — .human/reviews/hum-90.md"},
		Body:   "## Findings\nnone",
	}
	out := Render(m, []string{"verdict", "reviews"})
	assert.Equal(t,
		"[human:review-complete]\nverdict: pass\nreviews: HUM-89: pass — .human/reviews/hum-89.md\n  HUM-90: pass — .human/reviews/hum-90.md\n\n## Findings\nnone",
		out)
}

func TestParseBody_roundTrip(t *testing.T) {
	orig := Marker{
		Type:   "ready-for-review",
		Fields: map[string]string{"branch": "main", "commits": "abc, def", "daemon": "d-1"},
		Body:   "extra notes",
	}
	parsed, ok := ParseBody(Render(orig, []string{"branch", "commits", "daemon"}))
	require.True(t, ok)
	assert.Equal(t, orig, parsed)
}

func TestParseBody_multilineFieldRoundTrip(t *testing.T) {
	orig := Marker{
		Type:   "review-complete",
		Fields: map[string]string{"verdict": "pass", "reviews": "A: pass\nB: fail"},
	}
	parsed, ok := ParseBody(Render(orig, []string{"verdict", "reviews"}))
	require.True(t, ok)
	assert.Equal(t, orig, parsed)
}

func TestParseBody_multilineFieldWithEmbeddedBlankLine(t *testing.T) {
	orig := Marker{
		Type: "implementation-failed",
		Fields: map[string]string{
			"reason":    "verify budget spent",
			"kind":      "exhausted-fix-rounds",
			"evidence":  "$ go test ./...\n\nFAIL: TestX (0.01s)",
			"attempted": "retried twice",
			"release":   "a person resolves the flake",
		},
	}
	rendered := Render(orig, []string{"reason", "kind", "evidence", "attempted", "release"})
	parsed, ok := ParseBody(rendered)
	require.True(t, ok)
	// A blank line embedded in a field's value must stay inside that field —
	// not truncate it and spill the remaining fields into the body.
	assert.Equal(t, orig, parsed)
	assert.Empty(t, parsed.Body)
}

func TestParseBody_notAMarker(t *testing.T) {
	_, ok := ParseBody("just a regular comment")
	assert.False(t, ok)
	_, ok = ParseBody("")
	assert.False(t, ok)
}

func TestParseBody_headToken(t *testing.T) {
	m, ok := ParseBody("[human:bug-verify] NOT DONE\n\ndetails here")
	require.True(t, ok)
	assert.Equal(t, "bug-verify", m.Type)
	assert.Equal(t, "NOT DONE", m.Head)
	assert.Equal(t, "details here", m.Body)
}

func TestParseBody_unexpectedLineBecomesBody(t *testing.T) {
	m, ok := ParseBody("[human:plan]\nThis is prose, not a field line.\nMore prose.")
	require.True(t, ok)
	assert.Empty(t, m.Fields)
	assert.Equal(t, "This is prose, not a field line.\nMore prose.", m.Body)
}

func TestValidate_requiredFields(t *testing.T) {
	err := Validate(Marker{Type: "ready-for-review", Fields: map[string]string{"branch": "main"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required field")

	assert.NoError(t, Validate(Marker{Type: "ready-for-review", Fields: map[string]string{"branch": "main", "commits": "abc"}}))
}

// SC-5476: `review` is optional and, when present, closed to "inline" — the
// one value that says the posting run reviews the work itself.
func TestValidate_readyForReview_reviewInline(t *testing.T) {
	assert.NoError(t, Validate(Marker{Type: "ready-for-review", Fields: map[string]string{"branch": "b", "commits": "c", "review": "inline"}}))
}

func TestValidate_readyForReview_reviewUnknownValue(t *testing.T) {
	err := Validate(Marker{Type: "ready-for-review", Fields: map[string]string{"branch": "b", "commits": "c", "review": "later"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be one of inline")
}

func TestValidate_headEnum(t *testing.T) {
	assert.Error(t, Validate(Marker{Type: "bug-verdict"}))
	assert.Error(t, Validate(Marker{Type: "bug-verdict", Head: "maybe"}))
	assert.NoError(t, Validate(Marker{Type: "bug-verdict", Head: "confirmed"}))
	assert.NoError(t, Validate(Marker{Type: "bug-verify", Head: "NOT DONE"}))
}

// A blocker's kind is a closed set the contract names; a value outside it is
// refused at the post the way a bad head token is, and an absent kind stays
// legal because the daemon's own failure markers carry no blocker (SC-5250).
func TestValidate_blockerKindEnum(t *testing.T) {
	for _, typ := range []string{"planning-failed", "implementation-failed", "review-failed", "deploy-failed"} {
		err := Validate(Marker{Type: typ, Fields: map[string]string{"reason": "r", "kind": "missing-permision"}})
		require.Error(t, err, typ)
		assert.Contains(t, err.Error(), "missing-permission|unavailable-dependency", typ)
		assert.NoError(t, Validate(Marker{Type: typ, Fields: map[string]string{"reason": "r", "kind": "other"}}), typ)
		assert.NoError(t, Validate(Marker{Type: typ, Fields: map[string]string{"reason": "r"}}), typ)
		assert.Equal(t, BlockerKinds(), FieldValues(typ)["kind"], typ)
	}
	assert.Nil(t, FieldValues("deployed"))
}

func TestValidate_relatedHeadEnum(t *testing.T) {
	// The related record's head names which of the three required statements it
	// is; a head outside the enum would be a verdict no reader knows (SC-2405).
	assert.Error(t, Validate(Marker{Type: "related"}), "a verdict head is required")
	assert.Error(t, Validate(Marker{Type: "related", Head: "maybe"}), "outside the enum")
	for _, head := range []string{"found", "none", "incomplete"} {
		assert.NoError(t, Validate(Marker{Type: "related", Head: head}), head)
	}
	// related-started brackets the run's start and carries no head.
	assert.NoError(t, Validate(Marker{Type: "related-started"}))
}

// The idea drafter's provenance record must say whose words the description
// holds: the guard that protects a human's writing turns entirely on author,
// so a record without it protects nothing (SC-4608).
func TestValidate_ideaDraftRequiresAuthor(t *testing.T) {
	require.Error(t, Validate(Marker{Type: "idea-draft"}))
	assert.NoError(t, Validate(Marker{Type: "idea-draft", Fields: map[string]string{"author": "machine"}}))
	// idea-draft-started brackets the run's start and carries nothing.
	assert.NoError(t, Validate(Marker{Type: "idea-draft-started"}))
}

func TestValidate_ticketReviewVerdictEnum(t *testing.T) {
	// The gate ACTS on every outcome, so the head names what it did. A verdict
	// outside the enum would be a state no later stage knows how to read.
	assert.Error(t, Validate(Marker{Type: "ticket-review"}), "verdict head is required")
	assert.Error(t, Validate(Marker{Type: "ticket-review", Head: "needs-decision"}), "asking is not an outcome")
	for _, verdict := range []string{"ready", "reframed", "superseded", "escalated", "rejected"} {
		assert.NoError(t, Validate(Marker{Type: "ticket-review", Head: verdict}), verdict)
	}
	assert.NoError(t, Validate(Marker{Type: "ticket-review-started"}))
}

func TestValidate_shippedPartial(t *testing.T) {
	// A shipped-partial trace records a decision; both fields are required so a
	// trace missing either — the follow-on it points to or the criteria it defers
	// — is never postable (SC-2910).
	ok := Marker{Type: "shipped-partial", Fields: map[string]string{"follow-on": "SC-3001", "deferred": "CSV export"}}
	assert.NoError(t, Validate(ok))

	missingFollowOn := Marker{Type: "shipped-partial", Fields: map[string]string{"deferred": "CSV export"}}
	err := Validate(missingFollowOn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing a required field")

	missingDeferred := Marker{Type: "shipped-partial", Fields: map[string]string{"follow-on": "SC-3001"}}
	err = Validate(missingDeferred)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing a required field")
}

func TestKnownTypes_includesShippedPartial(t *testing.T) {
	assert.Contains(t, KnownTypes(), "shipped-partial")
}

func TestOptionalFields_needsPlanningEscalation(t *testing.T) {
	assert.Equal(t, []string{EscalationField}, OptionalFields("needs-planning"))
	assert.Empty(t, RequiredFields("needs-planning"), "an ordinary refusal requires nothing")
	assert.Empty(t, OptionalFields("plan"))
	assert.Empty(t, OptionalFields("no-such-marker"))
}

func TestValidate_optionalFieldNeverRequired(t *testing.T) {
	require.NoError(t, Validate(Marker{Type: "needs-planning"}))
	require.NoError(t, Validate(Marker{
		Type:   "needs-planning",
		Fields: map[string]string{EscalationField: EscalationPlanStuck, "reason": "…"},
	}))
}

func TestParseBody_escalationField(t *testing.T) {
	m, ok := ParseBody("[human:needs-planning]\nescalation: plan-stuck\nreason: x\nmachine: d1")
	require.True(t, ok)
	assert.Equal(t, EscalationPlanStuck, m.Fields[EscalationField])
}

func TestValidate_unknownTypeAllowed(t *testing.T) {
	assert.NoError(t, Validate(Marker{Type: "future-stage"}))
	assert.Error(t, Validate(Marker{Type: "Not A Type"}))
}

func TestLatest_newestWins(t *testing.T) {
	now := time.Now()
	comments := []tracker.Comment{
		{Body: "[human:plan]\n\nold plan", Created: now.Add(-time.Hour)},
		{Body: "unrelated", Created: now},
		{Body: "[human:plan]\n\nnew plan", Created: now.Add(-time.Minute)},
	}
	m, ok := Latest(comments, "plan")
	require.True(t, ok)
	assert.Equal(t, "new plan", m.Body)

	_, ok = Latest(comments, "deployed")
	assert.False(t, ok)
}

func TestAll_newestFirst(t *testing.T) {
	now := time.Now()
	comments := []tracker.Comment{
		{Body: "[human:review-started]", Created: now.Add(-time.Hour)},
		{Body: "not a marker", Created: now},
		{Body: "[human:deployed]\npr: http://x", Created: now.Add(-time.Minute)},
	}
	markers := All(comments)
	require.Len(t, markers, 2)
	assert.Equal(t, "deployed", markers[0].Type)
	assert.Equal(t, "review-started", markers[1].Type)
}

func TestKnownTypes_sortedAndComplete(t *testing.T) {
	types := KnownTypes()
	assert.Contains(t, types, "plan")
	assert.Contains(t, types, "ready-for-review")
	assert.Contains(t, types, "bug-verify")
	assert.IsIncreasing(t, types)
}

// The blocker contract lives in the protocol, not only in prose: every stage's
// *-failed marker advertises the four fields a needs-human-work stop carries,
// so `human fsm marker` names them (SC-5179).
func TestFailedMarkers_advertiseTheBlockerFields(t *testing.T) {
	for _, typ := range []string{"planning-failed", "implementation-failed", "review-failed", "deploy-failed"} {
		opt := OptionalFields(typ)
		for _, f := range BlockerFields() {
			if !slices.Contains(opt, f) {
				t.Errorf("%s: optional fields %v lack %q", typ, opt, f)
			}
		}
		if err := Validate(Marker{Type: typ, Fields: map[string]string{"reason": "r", "kind": "other", "evidence": "e", "attempted": "a", "release": "x"}}); err != nil {
			t.Errorf("%s: a marker carrying the blocker fields must validate: %v", typ, err)
		}
	}
}

// A nothing-to-do record must say why, and only from the set the board can
// label: an absent or invented reason is refused at the post, so no card can
// again render a refusal as "already shipped" (SC-5326).
func TestValidate_nothingToDoReason(t *testing.T) {
	err := Validate(Marker{Type: "nothing-to-do", Fields: map[string]string{"evidence": "PR #1"}})
	require.Error(t, err, "reason is required")
	err = Validate(Marker{Type: "nothing-to-do", Fields: map[string]string{"evidence": "PR #1", "reason": "shipped"}})
	require.Error(t, err, "reason outside the set")
	assert.Contains(t, err.Error(), "merged|duplicate|escalated|rejected")
	for _, r := range NothingToDoReasons() {
		assert.NoError(t, Validate(Marker{Type: "nothing-to-do", Fields: map[string]string{"evidence": "PR #1", "reason": r}}), r)
	}
	assert.Equal(t, NothingToDoReasons(), FieldValues("nothing-to-do")["reason"])
}
