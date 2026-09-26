package local

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

// The pipeline-aware half of the client: indexing markers as they are posted,
// refusing the ones the machine does not allow when strict, deriving the
// classification columns, and answering the capability interfaces.

var (
	_ tracker.MarkerIndexer     = (*Client)(nil)
	_ tracker.EventLister       = (*Client)(nil)
	_ tracker.ChangeCursor      = (*Client)(nil)
	_ tracker.PlacementRecorder = (*Client)(nil)
)

// fsmDoc loads the compiled-in state machine once per process. Strict mode is
// a second reader of the document; the daemon's conformance test remains the
// judge of whether the document is true of the code.
var fsmDoc = sync.OnceValues(func() (pipelinefsm.Document, error) {
	return pipelinefsm.Load()
})

// admitMarker decides whether a marker comment may be posted. In lenient mode
// (the default, and every other backend's only mode) the answer is always yes.
// In strict mode four things are refused, each the write-time form of a bug the
// pipeline has already had once:
//
//   - a marker the grammar rejects (unknown name shape, missing required field)
//   - the same *-started or *-failed marker twice in a row, which is a stage
//     declared started or dead twice for one event (SC-3857)
//   - a marker the state machine accepts from no state the history is consistent
//     with. Consistent, not pinned: a second [human:implementation-started] fits
//     both the plan-executing and the self-planning edge, and judging a later
//     marker against the branch replay happened to pick refused the one record
//     that ends a retried fix run cleanly (SC-5839)
//   - nothing, when the history is already off the machine: refusing then would
//     make a ticket the daemon lost track of unrecoverable, so it is logged and
//     let through
func (c *Client) admitMarker(ctx context.Context, number int64, key string, m marker.Marker) error {
	if !c.strict {
		return nil
	}
	if err := marker.Validate(m); err != nil {
		return errors.WrapWithDetails(err, "strict local tracker refused the marker", "key", key, "type", m.Type, "reason", err.Error())
	}
	history, err := c.st.markerTypes(ctx, number)
	if err != nil {
		return err
	}
	if n := len(history); n > 0 && history[n-1] == m.Type && isStageEvent(m.Type) {
		return errors.WithDetails("strict local tracker refused a repeated stage marker",
			"key", key, "type", m.Type, "reason", "the previous marker on this ticket is the same one")
	}
	doc, err := fsmDoc()
	if err != nil {
		return errors.WrapWithDetails(err, "loading the pipeline state machine for strict mode")
	}
	if isUnclassified(doc, m.Type) {
		return nil
	}
	replay := doc.Replay(history)
	if len(replay.Unaccounted) > 0 {
		log.Warn().Str("key", key).Str("type", m.Type).Str("first_refused", replay.Unaccounted[0].Marker).
			Msg("strict local tracker: history already left the machine; admitting the marker")
		return nil
	}
	if doc.Admits(replay, m.Type) {
		return nil
	}
	return refusalError(doc, key, m.Type, replay)
}

// refusalError says what was refused, where the history could have the item, and
// what the machine would have accepted there — in the message TEXT, because a
// poster reads only that: the structured details never reach a CLI caller, so a
// refusal that named nothing left the agent nothing to act on (SC-5839).
func refusalError(doc pipelinefsm.Document, key, markerType string, replay pipelinefsm.Replay) error {
	where := doc.Reachable(replay.States)
	allowed := allowedMarkers(doc, where)
	accepts := "no marker at all"
	if len(allowed) > 0 {
		accepts = strings.Join(allowed, ", ")
	}
	return errors.WithDetails(fmt.Sprintf(
		"strict local tracker refused [human:%s]: this ticket's history puts it at %s, which accepts %s",
		markerType, strings.Join(where, " or "), accepts),
		"key", key, "type", markerType, "state", replay.State,
		"could_be", strings.Join(where, ","), "allowed", strings.Join(allowed, ","))
}

func isStageEvent(markerType string) bool {
	return strings.HasSuffix(markerType, "-started") || strings.HasSuffix(markerType, "-failed")
}

func isUnclassified(doc pipelinefsm.Document, markerType string) bool {
	for _, u := range doc.Unclassified.Markers {
		if pipelinefsm.MarkerType(u) == markerType {
			return true
		}
	}
	return false
}

// allowedMarkers names what could have been posted instead, so a refusal is
// something the poster can act on rather than a closed door.
func allowedMarkers(doc pipelinefsm.Document, states []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, from := range states {
		for _, e := range doc.Out(from) {
			for _, part := range strings.Split(e.Marker, "|") {
				t := pipelinefsm.MarkerType(part)
				if t != "" && !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
	}
	return out
}

// indexComment records a comment in the event log and, when it is a marker,
// in the marker index. Called after the comment row exists so the index can
// point at it.
func (c *Client) indexComment(ctx context.Context, number int64, cr commentRow) error {
	m, ok := marker.ParseBody(cr.Body)
	if !ok {
		return c.st.record(ctx, number, eventComment, cr.Author, "")
	}
	if err := c.st.insertMarker(ctx, number, cr.ID, m.Type, m.Head, m.Fields, cr.Author, cr.CreatedAt); err != nil {
		return err
	}
	if err := c.st.record(ctx, number, eventMarker, cr.Author, m.Type); err != nil {
		return err
	}
	// A plan attached is the one marker that changes maturity.
	if m.Type == "plan" || m.Type == "plan-ready" {
		return c.refreshFacets(ctx, number)
	}
	return nil
}

// refreshFacets recomputes kind and maturity from the same conventions every
// other backend is read through (IsBug, IsSecurity, IsIdea), so the native
// columns can never disagree with what a caller of those sees.
func (c *Client) refreshFacets(ctx context.Context, number int64) error {
	r, err := c.st.getRow(ctx, number)
	if err != nil || r == nil {
		return err
	}
	issue := tracker.Issue{Type: r.Type, Labels: r.Labels}
	kind := "task"
	switch {
	case issue.IsIdea():
		kind = "idea"
	case issue.IsSecurity():
		kind = "security"
	case issue.IsBug():
		kind = "bug"
	case strings.EqualFold(strings.TrimSpace(r.Type), "feature") || strings.EqualFold(strings.TrimSpace(r.Type), "story"):
		kind = "feature"
	}
	maturity := "pm"
	if issue.IsIdea() {
		maturity = "idea"
	} else {
		markers, err := c.st.listMarkers(ctx, number)
		if err != nil {
			return err
		}
		for _, m := range markers {
			if m.Type == "plan" || m.Type == "plan-ready" {
				maturity = "planned"
				break
			}
		}
	}
	return c.st.setFacets(ctx, number, kind, maturity)
}

// ListMarkers implements tracker.MarkerIndexer.
func (c *Client) ListMarkers(ctx context.Context, key string) ([]tracker.IndexedMarker, error) {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return nil, err
	}
	rows, err := c.st.listMarkers(ctx, r.Number)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.IndexedMarker, len(rows))
	for i, m := range rows {
		out[i] = tracker.IndexedMarker{
			CommentID: itoa(m.CommentID), Type: m.Type, Head: m.Head, Fields: m.Fields, Author: m.Author, Created: m.CreatedAt,
		}
	}
	return out, nil
}

// ListEvents implements tracker.EventLister.
func (c *Client) ListEvents(ctx context.Context, key string) ([]tracker.Event, error) {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return nil, err
	}
	rows, err := c.st.listEvents(ctx, r.Number)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Event, len(rows))
	for i, e := range rows {
		out[i] = tracker.Event{ID: e.ID, Key: key, Kind: e.Kind, Actor: e.Actor, Detail: e.Detail, Created: e.CreatedAt}
	}
	return out, nil
}

// Version implements tracker.ChangeCursor.
func (c *Client) Version(ctx context.Context) (string, error) {
	return c.st.version(ctx)
}

// RecordPlacement implements tracker.PlacementRecorder. An unchanged placement
// writes nothing, so the board's own refresh never turns into a change the
// next refresh wakes up for.
func (c *Client) RecordPlacement(ctx context.Context, key, stage, state string) error {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	_, _, curStage, curState, err := c.st.facets(ctx, r.Number)
	if err != nil {
		return err
	}
	if curStage == stage && curState == state {
		return nil
	}
	if err := c.st.setPlacement(ctx, r.Number, stage, state); err != nil {
		return err
	}
	return c.st.record(ctx, r.Number, eventPlacement, "daemon", stage+"/"+state)
}

// attributes shapes the derived columns for Issue.Attributes; empty values are
// left out so a ticket the daemon has not placed yet carries no placement.
func attributes(kind, maturity, stage, state string) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{
		tracker.AttrKind: kind, tracker.AttrMaturity: maturity, tracker.AttrStage: stage, tracker.AttrState: state,
	} {
		if v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// backfillFacets classifies rows written before the classification columns
// existed. Runs once at open; a row that has a kind is left alone.
func (c *Client) backfillFacets(ctx context.Context) error {
	rows, err := c.st.db.QueryContext(ctx, "SELECT number FROM issues WHERE kind = ''")
	if err != nil {
		return errors.WrapWithDetails(err, "finding unclassified local issues")
	}
	var numbers []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return errors.WrapWithDetails(err, "scanning unclassified local issues")
		}
		numbers = append(numbers, n)
	}
	_ = rows.Close()
	for _, n := range numbers {
		if err := c.refreshFacets(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// backfillMarkers indexes comments written before the index existed. Runs
// once at open: every comment whose id the marker table does not know is
// parsed and, when it is a marker, indexed with its original author and time.
// Events are not invented for them — the log starts when the log started.
func (c *Client) backfillMarkers(ctx context.Context) error {
	rows, err := c.st.db.QueryContext(ctx, `SELECT c.id, c.issue, c.author, c.body, c.created_at FROM comments c
		WHERE NOT EXISTS (SELECT 1 FROM markers m WHERE m.comment_id = c.id) ORDER BY c.id ASC`)
	if err != nil {
		return errors.WrapWithDetails(err, "finding unindexed local comments")
	}
	type pending struct {
		id, issue int64
		author    string
		body      string
		created   string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.issue, &p.author, &p.body, &p.created); err != nil {
			_ = rows.Close()
			return errors.WrapWithDetails(err, "scanning unindexed local comments")
		}
		todo = append(todo, p)
	}
	_ = rows.Close()
	touched := map[int64]bool{}
	for _, p := range todo {
		m, ok := marker.ParseBody(p.body)
		if !ok {
			continue
		}
		if err := c.st.insertMarker(ctx, p.issue, p.id, m.Type, m.Head, m.Fields, p.author, parseTime(p.created)); err != nil {
			return err
		}
		touched[p.issue] = true
	}
	for n := range touched {
		if err := c.refreshFacets(ctx, n); err != nil {
			return err
		}
	}
	return nil
}
