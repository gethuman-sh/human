package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/ideadraft"
	"github.com/gethuman-sh/human/internal/tracker"
)

func newTestDescEditEngine(runner DescEditRunner, editor tracker.Editor) *DescEditEngine {
	return &DescEditEngine{
		Runner: runner,
		ResolveEditor: func() (tracker.Editor, error) {
			if editor == nil {
				return nil, nil
			}
			return editor, nil
		},
		TurnTimeout: time.Second,
	}
}

// waitForDescEditState polls engine.Status() until state is reached or the
// timeout elapses, since turns run asynchronously in goroutines.
func waitForDescEditState(t *testing.T, e *DescEditEngine, state DescEditState) DescEditStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var st DescEditStatus
	for time.Now().Before(deadline) {
		st = e.Status()
		if st.State == state {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %q, last state %q (error: %q)", state, st.State, st.Error)
	return st
}

func descProposalBlock(text string) string {
	return "[human:description-proposal]\n```markdown\n" + text + "\n```"
}

func TestDescEditEngine_StartCreatesAwaitingReplySession(t *testing.T) {
	runner := &fakeRunner{}
	e := newTestDescEditEngine(runner, nil)

	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	assert.Equal(t, DescEditAwaitingReply, st.State)
	assert.Empty(t, st.Transcript)
	assert.Equal(t, 0, runner.callCount())
}

// TestDescEditEngine_StartReattachesSameKey covers Start called twice for the
// same key with no Discard in between — e.g. a retried Start racing its own
// in-flight call while the modal is still open. This is NOT the AC6 scenario
// (see TestDescEditEngine_DiscardThenStartSameKeyStartsFresh below): AC6 is
// close-without-apply-then-reopen, which now runs Discard between the two
// Starts and must NOT reattach.
func TestDescEditEngine_StartReattachesSameKey(t *testing.T) {
	e := newTestDescEditEngine(&fakeRunner{}, nil)

	st1, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)
	st2, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)
	assert.Equal(t, st1.SessionID, st2.SessionID)
}

func TestDescEditEngine_StartFreshOnDifferentKey(t *testing.T) {
	e := newTestDescEditEngine(&fakeRunner{}, nil)

	st1, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)
	st2, err := e.Start(DescEditStartRequest{Key: "SC-2"})
	require.NoError(t, err)
	assert.NotEqual(t, st1.SessionID, st2.SessionID)
	assert.Equal(t, "SC-2", st2.Key)
}

func TestDescEditEngine_ReplyRejectsEmptyMessage(t *testing.T) {
	e := newTestDescEditEngine(&fakeRunner{}, nil)
	_, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: e.Status().SessionID, Message: "  "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestDescEditEngine_ReplyFirstTurnCarriesFullPrompt(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "ok", ResumeID: "cs-1"}}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "make it shorter"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	call := runner.callAt(0)
	assert.Equal(t, "", call.resumeID)
	assert.Contains(t, call.prompt, "Current description:")
	assert.Contains(t, call.prompt, "make it shorter")
}

func TestDescEditEngine_ReplySecondTurnIsPlainMessage(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "first reply", ResumeID: "cs-1"},
		{Reply: "second reply", ResumeID: "cs-1"},
	}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "first message"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "second message"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	call := runner.callAt(1)
	assert.NotEqual(t, "", call.resumeID)
	assert.Equal(t, "second message", call.prompt)
}

func TestDescEditEngine_ProposalMarkerSetsProposal(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "Here's a rewrite.\n" + descProposalBlock("New description text"), ResumeID: "cs-1"},
	}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	final := waitForDescEditState(t, e, DescEditAwaitingReply)

	assert.Equal(t, "New description text", final.Proposal)
	require.NotEmpty(t, final.Transcript)
	last := final.Transcript[len(final.Transcript)-1]
	assert.Equal(t, "agent", last.Role)
	assert.Equal(t, "Here's a rewrite.", last.Text)
}

func TestDescEditEngine_PlainChatDoesNotClearProposal(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"},
		{Reply: "just chatting, no marker here", ResumeID: "cs-1"},
	}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "thanks"})
	require.NoError(t, err)
	final := waitForDescEditState(t, e, DescEditAwaitingReply)

	assert.Equal(t, "Proposed text", final.Proposal)
}

func TestDescEditEngine_MalformedMarkerTriggersOneRepair(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "[human:description-proposal]\n```markdown\n\n```", ResumeID: "cs-1"},
		{Reply: descProposalBlock("Repaired text"), ResumeID: "cs-1"},
	}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	final := waitForDescEditState(t, e, DescEditAwaitingReply)

	assert.Equal(t, 2, runner.callCount())
	assert.Equal(t, "Repaired text", final.Proposal)
}

func TestDescEditEngine_MalformedMarkerErrorsAfterRepairFails(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "[human:description-proposal]\n```markdown\n\n```", ResumeID: "cs-1"},
		{Reply: "[human:description-proposal]\n```markdown\n\n```", ResumeID: "cs-1"},
	}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	final := waitForDescEditState(t, e, DescEditError)

	assert.Contains(t, final.Error, "malformed")
}

func TestDescEditEngine_ApplyRejectsWithoutProposal(t *testing.T) {
	editor := &fakeEditor{returned: &tracker.Issue{Key: "SC-1"}}
	e := newTestDescEditEngine(&fakeRunner{}, editor)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)

	_, err = e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no proposed rewrite")
}

func TestDescEditEngine_ApplyWritesOnlyDescription(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	editor := &fakeEditor{returned: &tracker.Issue{Key: "SC-1", URL: "https://x/1"}}
	e := newTestDescEditEngine(runner, editor)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	applied, err := e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.NoError(t, err)

	key, opts := editor.captured()
	assert.Equal(t, "SC-1", key)
	require.NotNil(t, opts.Description)
	assert.Equal(t, "Proposed text", *opts.Description)
	assert.Nil(t, opts.Title)
	assert.Empty(t, opts.AddLabels)
	assert.Empty(t, opts.RemoveLabels)
	assert.Equal(t, DescEditApplied, applied.State)
	assert.Equal(t, "https://x/1", applied.AppliedURL)
}

// countingEditor wraps a tracker.Editor and counts EditIssue invocations —
// fakeEditor (ideation_test.go) only captures the last call, not a count,
// which is exactly what the idempotency guarantee needs to verify.
type countingEditor struct {
	inner tracker.Editor
	mu    sync.Mutex
	calls int
}

func (c *countingEditor) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.EditIssue(ctx, key, opts)
}

func (c *countingEditor) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestDescEditEngine_ApplyIsIdempotent(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	inner := &fakeEditor{returned: &tracker.Issue{Key: "SC-1", URL: "https://x/1"}}
	editor := &countingEditor{inner: inner}
	e := newTestDescEditEngine(runner, editor)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	first, err := e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.NoError(t, err)
	second, err := e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, 1, editor.callCount())
}

func TestDescEditEngine_ApplyFailurePreservesProposal(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	editor := &fakeEditor{err: assert.AnError}
	e := newTestDescEditEngine(runner, editor)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	_, err = e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.Error(t, err)

	final := e.Status()
	assert.Equal(t, DescEditAwaitingReply, final.State)
	assert.Equal(t, "Proposed text", final.Proposal)
}

func TestDescEditEngine_ApplyNoEditorConfigured(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	e := &DescEditEngine{Runner: runner, TurnTimeout: time.Second}
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	_, err = e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PM ticket editor configured")
}

// TestDescEditEngine_DiscardEndsMatchingSession covers the modal's plain
// close path (Close/Escape/backdrop before any Apply): Discard ends the
// session outright, not merely clears its proposal — Status reports None
// afterward, same as if nothing had ever started.
func TestDescEditEngine_DiscardEndsMatchingSession(t *testing.T) {
	e := newTestDescEditEngine(&fakeRunner{}, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	discarded := e.Discard(DescEditDiscardRequest{SessionID: st.SessionID})
	assert.Equal(t, DescEditNone, discarded.State)
	assert.Equal(t, DescEditNone, e.Status().State)
}

// TestDescEditEngine_DiscardThenStartSameKeyStartsFresh is the literal AC6
// scenario the review flagged: a proposal is set (session left in
// AwaitingReply, exactly the state Start's same-key reattach checks for),
// the modal closes without Apply (Discard runs), and reopening the SAME
// ticket must NOT reattach to the stale proposal/transcript — it must start
// a brand new session.
func TestDescEditEngine_DiscardThenStartSameKeyStartsFresh(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	e := newTestDescEditEngine(runner, nil)
	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	live := waitForDescEditState(t, e, DescEditAwaitingReply)
	require.Equal(t, "Proposed text", live.Proposal, "sanity: the session really does carry a pending proposal before close")

	// The modal's close path: Discard, then (on reopen) Start for the SAME key.
	e.Discard(DescEditDiscardRequest{SessionID: live.SessionID})
	reopened, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)

	assert.NotEqual(t, live.SessionID, reopened.SessionID, "reopen must start a NEW session, not reattach to the discarded one")
	assert.Empty(t, reopened.Proposal, "no stale proposal should survive close-without-apply")
	assert.Empty(t, reopened.Transcript, "no stale chat history should survive close-without-apply")
}

// TestDescEditEngine_DiscardIsNoopForStaleSessionID covers the fire-and-forget
// call racing a fresh Start (e.g. the user closed ticket A and immediately
// opened ticket B before A's Discard landed) — it must never tear down a
// session it no longer names.
func TestDescEditEngine_DiscardIsNoopForStaleSessionID(t *testing.T) {
	e := newTestDescEditEngine(&fakeRunner{}, nil)
	_, err := e.Start(DescEditStartRequest{Key: "SC-1"})
	require.NoError(t, err)
	current, err := e.Start(DescEditStartRequest{Key: "SC-2", Restart: true})
	require.NoError(t, err)

	e.Discard(DescEditDiscardRequest{SessionID: "some-stale-or-unknown-id"})

	assert.Equal(t, current.SessionID, e.Status().SessionID)
	assert.Equal(t, DescEditAwaitingReply, e.Status().State)
}

func TestParseDescProposalBlock_Found(t *testing.T) {
	proposal, stripped, found, err := parseDescProposalBlock("text\n" + descProposalBlock("Hello"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "Hello", proposal)
	assert.Equal(t, "text", stripped)
}

func TestParseDescProposalBlock_EmptyBlockErrors(t *testing.T) {
	_, _, found, err := parseDescProposalBlock("[human:description-proposal]\n```markdown\n\n```")
	assert.True(t, found)
	require.Error(t, err)
}

func TestParseDescProposalBlock_NoMarker(t *testing.T) {
	_, _, found, err := parseDescProposalBlock("just chatting")
	assert.False(t, found)
	assert.NoError(t, err)
}

// recordingCommenter captures the provenance record Apply posts.
type recordingCommenter struct {
	mu     sync.Mutex
	bodies []string
	err    error
}

func (c *recordingCommenter) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}

func (c *recordingCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	c.bodies = append(c.bodies, body)
	return &tracker.Comment{Body: body}, nil
}

func (c *recordingCommenter) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

// appliedWithCommenter drives a session to an applied proposal, recording what
// the engine posted alongside the write.
func appliedWithCommenter(t *testing.T, commenter *recordingCommenter) DescEditStatus {
	t.Helper()
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: descProposalBlock("Proposed text"), ResumeID: "cs-1"}}}
	editor := &fakeEditor{returned: &tracker.Issue{Key: "SC-1", URL: "https://x/1"}}
	e := newTestDescEditEngine(runner, editor)
	e.ResolveCommenter = func() (tracker.Commenter, error) { return commenter, nil }

	st, err := e.Start(DescEditStartRequest{Key: "SC-1", CurrentDescription: "old"})
	require.NoError(t, err)
	_, err = e.Reply(DescEditReplyRequest{SessionID: st.SessionID, Message: "rewrite it"})
	require.NoError(t, err)
	waitForDescEditState(t, e, DescEditAwaitingReply)

	applied, err := e.Apply(DescEditApplyRequest{SessionID: st.SessionID})
	require.NoError(t, err)
	return applied
}

// The applied text is the user's, and pinning it as human-authored is what
// stops a redraft armed before promotion from writing over their edit.
func TestDescEditApply_PinsAHumanFingerprint(t *testing.T) {
	commenter := &recordingCommenter{}
	applied := appliedWithCommenter(t, commenter)

	assert.Equal(t, DescEditApplied, applied.State)
	bodies := commenter.all()
	require.Len(t, bodies, 1)
	assert.Contains(t, bodies[0], IdeaDraftHeader)
	assert.Contains(t, bodies[0], "author: human")
	assert.Contains(t, bodies[0], "description: "+ideadraft.Fingerprint("Proposed text"))
	assert.NotContains(t, bodies[0], "source:", "a human record has no input the machine could redraft from")
}

func TestDescEditApply_SurvivesAFailedFingerprintPost(t *testing.T) {
	commenter := &recordingCommenter{err: assert.AnError}
	applied := appliedWithCommenter(t, commenter)

	assert.Equal(t, DescEditApplied, applied.State, "a failed record must not turn a saved description into a failed apply")
	assert.Equal(t, "https://x/1", applied.AppliedURL)
}

func TestDescEditSystemPrompt_PromotedWidensTheRemit(t *testing.T) {
	prompt := descEditSystemPrompt("a drafted description [TBA: for whom?]", true)

	assert.Contains(t, prompt, "challenge the premise")
	assert.Contains(t, prompt, "NEVER answer a [TBA:] yourself")
	assert.NotContains(t, prompt, "Never suggest or discuss changing the title, acceptance criteria structure",
		"the copy-editor gate is exactly what promotion must not keep")
}

func TestDescEditSystemPrompt_UnpromotedIsUnchanged(t *testing.T) {
	prompt := descEditSystemPrompt("a description", false)

	assert.Contains(t, prompt, "Your ONLY job is proposing rewrites")
	assert.Contains(t, prompt, "Never suggest or discuss changing the title, acceptance criteria structure")
	assert.NotContains(t, prompt, "challenge the premise")
	assert.NotContains(t, prompt, "[TBA:")
}
