package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
