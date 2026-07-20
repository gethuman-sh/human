package marker

import (
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

func TestValidate_headEnum(t *testing.T) {
	assert.Error(t, Validate(Marker{Type: "bug-verdict"}))
	assert.Error(t, Validate(Marker{Type: "bug-verdict", Head: "maybe"}))
	assert.NoError(t, Validate(Marker{Type: "bug-verdict", Head: "confirmed"}))
	assert.NoError(t, Validate(Marker{Type: "bug-verify", Head: "NOT DONE"}))
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
