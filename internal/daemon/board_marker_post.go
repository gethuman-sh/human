package daemon

import (
	"context"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// postMarker renders a marker through the protocol's own writer and posts it.
//
// The daemon writes most of the markers in the system and used to build them by
// concatenating a header constant with a line of prose. That put the biggest
// writer outside the one place that knows the format: `human marker post`
// validates required fields, head-token enums and the decision-block contract,
// and the daemon validated nothing. The results were not hypothetical — it
// posted deploy-failed markers with no reason, deployed markers that said
// nothing about how the work shipped, and single-answer decision blocks the
// protocol explicitly forbids ([SC-3889]).
//
// A validation failure here is a programming error, not a runtime condition, so
// it is logged loudly and the marker is posted anyway. Dropping it would be the
// worse failure: the reader sees no marker at all and the card stalls with
// nothing to explain it, which is exactly the silent stall this protocol exists
// to prevent. The test suite is where an invalid marker is meant to be caught
// (TestDaemonPostedMarkersSatisfyTheirContract).
func postMarker(ctx context.Context, c tracker.Commenter, key string, m marker.Marker, fieldOrder ...string) error {
	if err := marker.Validate(m); err != nil {
		log.Error().Err(err).Str("type", m.Type).Str("key", key).
			Msg("posting a marker that does not satisfy its own contract")
	}
	_, err := c.AddComment(ctx, key, marker.Render(m, fieldOrder))
	return err
}

// markerBody renders without posting, for the callers that hand a body to
// something else (a signing wrapper, a test double, a queued write).
func markerBody(m marker.Marker, fieldOrder ...string) string {
	if err := marker.Validate(m); err != nil {
		log.Error().Err(err).Str("type", m.Type).
			Msg("composing a marker that does not satisfy its own contract")
	}
	return marker.Render(m, fieldOrder)
}

// DeployedBody renders the [human:deployed] marker for work that shipped
// through a pull request.
//
// Exported because the daemon command wires a confirm-shipped probe that posts
// this marker from outside the package; it used to build the body there by
// concatenation, which put a second author of the format one edit away from the
// one that owns it.
func DeployedBody(prURL string) string {
	return markerBody(marker.Marker{Type: MarkerDeployed, Fields: fields("pr", prURL)})
}

// RunCancelledBody renders the [human:run-cancelled] marker: closing the ticket
// stopped work that was still running on it.
//
// It names the stage and the agent rather than only the fact, because the reader
// this exists for is looking at a closed ticket months later asking what
// happened to a run that left commits, a worktree, or nothing at all. The stop
// is not a failure — a person called the work off — so this is a record, never
// a red.
func RunCancelledBody(stage BoardStage, agentName string) string {
	return markerBody(marker.Marker{
		Type:   MarkerRunCancelled,
		Fields: fields("stage", string(stage), "agent", agentName),
		Body:   "the ticket was closed while this stage was running, so its agent was stopped",
	}, "stage", "agent")
}

// optionsMarker composes a decision block — the stage that resumes once it is
// answered, the context that raised it, and one field per answer — and returns
// the field order alongside it.
//
// The order is part of the wire format, not presentation: a numbered answer id
// is not a field name the marker grammar accepts, so the reader takes the first
// one as the start of the body and everything after it as prose. stage and
// context therefore have to be written before the answers or they are not
// fields at all — which is how a decision block ended up validating as one with
// no stage. validateOptions counts answers in both halves, so the answers
// themselves are read correctly either way.
func optionsMarker(stage BoardStage, context string, opts []BoardOption) (marker.Marker, []string) {
	m := marker.Marker{
		Type:   MarkerOptions,
		Fields: fields("stage", string(stage), "context", context),
	}
	order := []string{"stage", "context"}
	for _, o := range opts {
		m.Fields[o.ID] = o.Label
		order = append(order, o.ID)
	}
	return m, order
}

// failureMarker builds a *-failed marker from a composed diagnosis.
//
// The split is not cosmetic: the card badge shows one line and the detail pane
// shows the rest, so the headline goes in `reason` (the field every *-failed
// spec requires) and the detail stays prose in the body. Keeping the whole
// diagnosis in `reason` would fold its blank line into a field continuation and
// silently truncate the field block at the first empty line; keeping it all in
// the body would post a marker with no reason at all, which is what the daemon
// did before. failureBody recomposes the two halves for the reader.
func failureMarker(markerType, diagnosis string) marker.Marker {
	headline, detail, _ := strings.Cut(strings.TrimSpace(diagnosis), "\n")
	return marker.Marker{
		Type:   markerType,
		Fields: fields("reason", strings.TrimSpace(headline)),
		Body:   strings.TrimSpace(detail),
	}
}

// fields is shorthand for the one-or-two field markers that make up most of
// what the daemon posts.
func fields(pairs ...string) map[string]string {
	out := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}
