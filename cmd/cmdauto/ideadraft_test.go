package cmdauto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/ideadraft"
	"github.com/gethuman-sh/human/internal/tracker"
)

// draftStub is a tracker holding one idea in memory: a write updates the issue
// the next GetIssue returns, so a test can run the command twice and see what
// the second run decides about the first run's work.
type draftStub struct {
	semanticStub
	issue tracker.Issue
	// stripTBA models a tracker whose editor rewrites the stored text — the
	// round-trip this ticket could not verify from code.
	stripTBA bool
	// commentErr models the tracker accepting the description edit and then
	// refusing the provenance comment — the half-written run.
	commentErr error
	comments   []tracker.Comment
	edits      []tracker.EditOptions
	posted     []string
	now        time.Time
}

func (d *draftStub) GetIssue(context.Context, string) (*tracker.Issue, error) {
	issue := d.issue
	if d.stripTBA {
		issue.Description = strings.ReplaceAll(issue.Description, ideadraft.TBAToken, "")
	}
	return &issue, nil
}

func (d *draftStub) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return d.comments, nil
}

func (d *draftStub) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	if d.commentErr != nil {
		return nil, d.commentErr
	}
	d.now = d.now.Add(time.Minute)
	c := tracker.Comment{Body: body, Created: d.now}
	d.comments = append(d.comments, c)
	d.posted = append(d.posted, body)
	return &c, nil
}

func (d *draftStub) EditIssue(_ context.Context, _ string, opts tracker.EditOptions) (*tracker.Issue, error) {
	d.edits = append(d.edits, opts)
	if opts.Description != nil {
		d.issue.Description = *opts.Description
	}
	return &d.issue, nil
}

func newDraftStub(description string) *draftStub {
	return &draftStub{
		issue: tracker.Issue{Key: "SC-1", Title: "a raw idea", Description: description, Labels: []string{"human/idea"}},
		now:   time.Unix(1000, 0),
	}
}

func draftFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draft.md")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

func runDraft(t *testing.T, p tracker.Provider, opts IdeaDraftOpts) (ideaDraftResult, string) {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, RunIdeaDraft(context.Background(), p, &buf, "SC-1", opts))
	var res ideaDraftResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &res))
	return res, buf.String()
}

func TestRunIdeaDraft_WritesAndRecords(t *testing.T) {
	p := newDraftStub("")
	text := "## Problem\n\nSomething [TBA: for whom?]\n"
	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: draftFile(t, text)})

	assert.True(t, res.Written)
	assert.Equal(t, string(ideadraft.VerdictWrite), res.Decision)
	assert.Equal(t, 1, res.TBA)
	assert.True(t, res.RoundtripOK)

	require.Len(t, p.edits, 1)
	require.NotNil(t, p.edits[0].Description)
	assert.Equal(t, text, *p.edits[0].Description)
	assert.Nil(t, p.edits[0].Title, "a draft touches the description and nothing else")
	assert.Nil(t, p.edits[0].AddLabels)
	assert.Nil(t, p.edits[0].RemoveLabels)

	require.Len(t, p.posted, 1)
	assert.Contains(t, p.posted[0], "[human:idea-draft]")
	assert.Contains(t, p.posted[0], "author: machine")
	assert.Contains(t, p.posted[0], "description: "+ideadraft.Fingerprint(text))
	assert.Contains(t, p.posted[0], "source: "+ideadraft.SourceFingerprint("a raw idea"))
}

func TestRunIdeaDraft_StandsDownWithoutWriting(t *testing.T) {
	p := newDraftStub("words a person wrote")
	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: draftFile(t, "a draft")})

	assert.False(t, res.Written)
	assert.Equal(t, string(ideadraft.VerdictStandDown), res.Decision)
	assert.Empty(t, p.edits)
	assert.Empty(t, p.posted)
}

// The acceptance criterion's own test: once a human has edited the description,
// no automatic redraft ever writes to that ticket again.
func TestRunIdeaDraft_TwoRedraftsAfterAHumanEdit(t *testing.T) {
	p := newDraftStub("")
	file := draftFile(t, "the machine's draft [TBA: who?]")
	runDraft(t, p, IdeaDraftOpts{DescriptionFile: file})
	require.Len(t, p.edits, 1)

	p.issue.Description = "the machine's draft, corrected by hand"
	p.issue.Title = "a raw idea, retitled"

	for range 2 {
		res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: file})
		assert.False(t, res.Written)
		assert.Equal(t, string(ideadraft.VerdictStandDown), res.Decision)
	}
	assert.Len(t, p.edits, 1, "exactly one write across all three runs")
}

func TestRunIdeaDraft_ReportsRoundTripLoss(t *testing.T) {
	p := newDraftStub("")
	p.stripTBA = true
	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: draftFile(t, "x [TBA: who?]")})

	assert.True(t, res.Written)
	assert.False(t, res.RoundtripOK, "a tracker that rewrote the bracket text must be reported, not assumed away")
}

func TestRunIdeaDraft_CheckWritesNothing(t *testing.T) {
	p := newDraftStub("")
	res, _ := runDraft(t, p, IdeaDraftOpts{Check: true})

	assert.Equal(t, string(ideadraft.VerdictWrite), res.Decision)
	assert.False(t, res.Written)
	assert.Empty(t, p.edits)
	assert.Empty(t, p.posted)
}

func TestRunIdeaDraft_StandDownRecordsOnceAndIsFinal(t *testing.T) {
	p := newDraftStub("words a person wrote")
	runDraft(t, p, IdeaDraftOpts{StandDown: true})
	require.Len(t, p.posted, 1)
	assert.Contains(t, p.posted[0], "author: human")

	runDraft(t, p, IdeaDraftOpts{StandDown: true})
	assert.Len(t, p.posted, 1, "a second stand-down for the same bytes adds nothing")

	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: draftFile(t, "a redraft")})
	assert.False(t, res.Written)
	assert.Empty(t, p.edits)
}

func TestRunIdeaDraft_RefusesAnEmptyDescription(t *testing.T) {
	p := newDraftStub("")
	var buf bytes.Buffer
	err := RunIdeaDraft(context.Background(), p, &buf, "SC-1", IdeaDraftOpts{DescriptionFile: draftFile(t, "  \n")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to write an empty description")
	assert.Empty(t, p.edits)
}

func TestRunIdeaDraft_ReadsStdin(t *testing.T) {
	p := newDraftStub("")
	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: "-", Stdin: strings.NewReader("from stdin [TBA: when?]")})
	assert.True(t, res.Written)
	assert.Equal(t, 1, res.TBA)
}

func TestValidateIdeaDraftOpts(t *testing.T) {
	require.Error(t, validateIdeaDraftOpts(IdeaDraftOpts{}))
	require.Error(t, validateIdeaDraftOpts(IdeaDraftOpts{Check: true, StandDown: true}))
	require.Error(t, validateIdeaDraftOpts(IdeaDraftOpts{Check: true, DescriptionFile: "x"}))
	assert.NoError(t, validateIdeaDraftOpts(IdeaDraftOpts{Check: true}))
	assert.NoError(t, validateIdeaDraftOpts(IdeaDraftOpts{StandDown: true}))
	assert.NoError(t, validateIdeaDraftOpts(IdeaDraftOpts{DescriptionFile: "x"}))
}

// A failed run leaves the description byte-identical to what it was — the
// acceptance criterion, and the reason the provenance comment is not allowed to
// fail quietly: machine-written words no record claims read as a human's
// forever after.
func TestRunIdeaDraft_RestoresTheDescriptionWhenTheRecordFails(t *testing.T) {
	p := newDraftStub("")
	p.commentErr = assert.AnError

	var buf bytes.Buffer
	err := RunIdeaDraft(context.Background(), p, &buf, "SC-1",
		IdeaDraftOpts{DescriptionFile: draftFile(t, "a draft [TBA: who?]")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the description was put back")
	assert.Empty(t, p.issue.Description, "the ticket is as the run found it")
	require.Len(t, p.edits, 2, "the write and the undo")
	require.NotNil(t, p.edits[1].Description)
	assert.Empty(t, *p.edits[1].Description)
	assert.Empty(t, p.posted)

	// And the next run is still free to draft: nothing on the ticket claims a
	// human wrote anything.
	p.commentErr = nil
	res, _ := runDraft(t, p, IdeaDraftOpts{DescriptionFile: draftFile(t, "a second try")})
	assert.True(t, res.Written)
}

// A rollback that itself fails must say both things: the run failed AND the
// ticket is not as it was.
func TestRunIdeaDraft_ReportsAFailedRestore(t *testing.T) {
	p := &failingEditStub{draftStub: newDraftStub(""), failEditAfter: 1}
	p.commentErr = assert.AnError

	var buf bytes.Buffer
	err := RunIdeaDraft(context.Background(), p, &buf, "SC-1",
		IdeaDraftOpts{DescriptionFile: draftFile(t, "a draft")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be put back")
}

// failingEditStub lets the first EditIssue through and refuses the rest, so the
// undo fails while the write succeeded.
type failingEditStub struct {
	*draftStub
	failEditAfter int
	edited        int
}

func (f *failingEditStub) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	f.edited++
	if f.edited > f.failEditAfter {
		return nil, assert.AnError
	}
	return f.draftStub.EditIssue(ctx, key, opts)
}

// A ticket that lost its idea label is NOT a ticket whose description a human
// wrote: recording one there would freeze the words of a re-labelled ticket
// forever.
func TestRunIdeaDraft_StandDownOnANonIdeaRecordsNothing(t *testing.T) {
	p := newDraftStub("machine words")
	p.issue.Labels = nil

	res, _ := runDraft(t, p, IdeaDraftOpts{StandDown: true})

	assert.Equal(t, string(ideadraft.VerdictStandDown), res.Decision)
	assert.Equal(t, string(ideadraft.ReasonNotAnIdea), res.Reason)
	assert.Empty(t, p.posted, "no human record for a stand-down about labels")

	// Re-labelled as an idea, the description is still unclaimed — the guard
	// stands down on provenance, not on a record this run must not have left.
	p.issue.Labels = []string{"human/idea"}
	again, _ := runDraft(t, p, IdeaDraftOpts{Check: true})
	assert.Equal(t, string(ideadraft.ReasonUnknownProvenance), again.Reason)
}
