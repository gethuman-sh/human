package local

import (
	"context"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Test fixtures. A local tracker on ":memory:" is a real backend the daemon's
// passes can be driven against, which is what replaces a stubbed commenter: the
// markers it reads are the ones the code under test wrote, through the same
// path a live ticket takes.

// OpenMemory opens a private in-memory tracker for a test.
func OpenMemory(prefix, user string, opts ...Option) (*Client, error) {
	return Open(":memory:", prefix, user, opts...)
}

// SeedMarkers creates a ticket and posts the given markers on it in order,
// through AddComment, so the index, the event log and (when strict) the
// machine all see them exactly as a live post. Returns the new key.
func SeedMarkers(ctx context.Context, c *Client, title string, markers []marker.Marker) (string, error) {
	issue, err := c.CreateIssue(ctx, &tracker.Issue{Title: title})
	if err != nil {
		return "", err
	}
	for _, m := range markers {
		if _, err := c.AddComment(ctx, issue.Key, marker.Render(m, nil)); err != nil {
			return "", errors.WrapWithDetails(err, "seeding marker", "key", issue.Key, "type", m.Type)
		}
	}
	return issue.Key, nil
}

// SeedTrace replays one recorded history from the FSM corpus — marker types
// only, as the corpus keeps them — onto a fresh ticket. The result is the same
// thread the daemon would have left, minus the bodies the corpus never stored,
// so a test can start where a real ticket got stuck. The bodies are what the
// grammar validates, so the seed itself is admitted leniently and strictness
// resumes for the markers the test under way posts afterwards.
func SeedTrace(ctx context.Context, c *Client, trace pipelinefsm.Trace) (string, error) {
	strict := c.strict
	c.strict = false
	defer func() { c.strict = strict }()
	markers := make([]marker.Marker, len(trace.Markers))
	for i, t := range trace.Markers {
		markers[i] = marker.Marker{Type: pipelinefsm.MarkerType(t)}
	}
	return SeedMarkers(ctx, c, trace.Key, markers)
}
