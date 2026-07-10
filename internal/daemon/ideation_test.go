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

// fakeRunner returns queued turns/errors in order, recording each call.
type fakeRunner struct {
	mu    sync.Mutex
	turns []IdeationTurn
	errs  []error
	calls []struct{ resumeID, prompt string }
}

func (f *fakeRunner) Run(_ context.Context, resumeID, prompt string) (IdeationTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ resumeID, prompt string }{resumeID, prompt})
	idx := len(f.calls) - 1
	var turn IdeationTurn
	var err error
	if idx < len(f.turns) {
		turn = f.turns[idx]
	}
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	return turn, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) callAt(i int) struct{ resumeID, prompt string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// fakeCreator is a function-backed tracker.Creator capturing the issue.
type fakeCreator struct {
	mu       sync.Mutex
	captured *tracker.Issue
	created  *tracker.Issue
	err      error
}

func (f *fakeCreator) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = issue
	if f.err != nil {
		return nil, f.err
	}
	return f.created, nil
}

func (f *fakeCreator) capturedIssue() *tracker.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captured
}

func newFakeCreator() *fakeCreator {
	return &fakeCreator{created: &tracker.Issue{Key: "SC-999", URL: "https://app.shortcut.com/x/story/999"}}
}

func newTestEngine(runner IdeationRunner, creator tracker.Creator, project string, notify func()) *IdeationEngine {
	return &IdeationEngine{
		Runner: runner,
		ResolveCreator: func() (tracker.Creator, string, error) {
			return creator, project, nil
		},
		Notify:      notify,
		TurnTimeout: time.Second,
	}
}

// waitForState polls engine.Status() until state is reached or the timeout
// elapses, since turns run asynchronously in goroutines.
func waitForState(t *testing.T, e *IdeationEngine, state IdeationState) IdeationStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var st IdeationStatus
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

func markerBlock(title, description string) string {
	return "[human:ideation-ticket]\n```json\n{\"title\": \"" + title + "\", \"description\": \"" + description + "\"}\n```"
}

func questionBlock(text, kind string, options ...string) string {
	optJSON := "["
	for i, o := range options {
		if i > 0 {
			optJSON += ", "
		}
		optJSON += "\"" + o + "\""
	}
	optJSON += "]"
	return "[human:ideation-question]\n```json\n{\"text\": \"" + text + "\", \"options\": " + optJSON + ", \"kind\": \"" + kind + "\"}\n```"
}

func newTestGuidedEngine(runner IdeationRunner, creator tracker.Creator, project string, notify func()) *IdeationEngine {
	return newTestEngine(runner, creator, project, notify)
}

func TestIdeationStart_EmptySeed(t *testing.T) {
	e := newTestEngine(&fakeRunner{}, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: " "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "seed must not be empty")
}

func TestIdeationStart_FirstTurnAsksQuestion(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "Q1?", ResumeID: "cs-1"}}}
	e := newTestEngine(runner, newFakeCreator(), "PRJ", nil)

	_, err := e.Start(IdeationStartRequest{Seed: "my idea"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationAwaitingReply)
	require.Len(t, st.Transcript, 2)
	assert.Equal(t, "user", st.Transcript[0].Role)
	assert.Equal(t, "agent", st.Transcript[1].Role)
	assert.Equal(t, "Q1?", st.Transcript[1].Text)

	require.Equal(t, 1, runner.callCount())
	call := runner.callAt(0)
	assert.Empty(t, call.resumeID)
	assert.Contains(t, call.prompt, "my idea")
	assert.Contains(t, call.prompt, "[human:ideation-ticket]")
}

func TestIdeationStart_AttachWhileActive(t *testing.T) {
	runner := &fakeRunner{} // never returns, keeping the session in "thinking"
	e := &IdeationEngine{Runner: runner, TurnTimeout: time.Second}
	e.mu.Lock()
	e.sess = &ideationSession{id: "existing", state: IdeationAwaitingReply}
	e.mu.Unlock()

	st, err := e.Start(IdeationStartRequest{Seed: "x"})
	require.NoError(t, err)
	assert.Equal(t, "existing", st.SessionID)
	assert.Equal(t, 0, runner.callCount())
}

func TestIdeationStart_RestartAbandons(t *testing.T) {
	block := make(chan struct{})
	runner := &blockingRunner{release: block}
	e := &IdeationEngine{Runner: runner, TurnTimeout: 2 * time.Second}
	e.mu.Lock()
	e.sess = &ideationSession{id: "old", state: IdeationThinking}
	e.mu.Unlock()

	st, err := e.Start(IdeationStartRequest{Seed: "y", Restart: true})
	require.NoError(t, err)
	assert.NotEqual(t, "old", st.SessionID)
	assert.Equal(t, IdeationThinking, st.State)

	close(block) // let the old turn (if it were running) complete; here it's a no-op guard
}

// blockingRunner completes only after release is closed, used to simulate a
// stale in-flight turn from an abandoned session.
type blockingRunner struct {
	release chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, _, _ string) (IdeationTurn, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return IdeationTurn{Reply: "late", ResumeID: "late-id"}, nil
}

func TestIdeationReply_MultiTurnLoop(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "Q1?", ResumeID: "cs-1"},
		{Reply: "Q2?", ResumeID: "cs-2"},
	}}
	e := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)
	st := waitForState(t, e, IdeationAwaitingReply)

	_, err = e.Reply(IdeationReplyRequest{SessionID: st.SessionID, Message: "answer 1"})
	require.NoError(t, err)

	st = waitForState(t, e, IdeationAwaitingReply)
	require.Len(t, st.Transcript, 4)

	require.Equal(t, 2, runner.callCount())
	call := runner.callAt(1)
	assert.Equal(t, "cs-1", call.resumeID)
	assert.Equal(t, "answer 1", call.prompt)
}

func TestIdeationReply_WrongSession(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "Q1?", ResumeID: "cs-1"}}}
	e := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)
	waitForState(t, e, IdeationAwaitingReply)

	_, err = e.Reply(IdeationReplyRequest{SessionID: "nope", Message: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching ideation session")
}

func TestIdeationReply_WhileThinking(t *testing.T) {
	block := make(chan struct{})
	runner := &blockingRunner{release: block}
	defer close(block)
	e := newTestEngine(runner, newFakeCreator(), "PRJ", nil)
	st, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)
	require.Equal(t, IdeationThinking, st.State)

	_, err = e.Reply(IdeationReplyRequest{SessionID: st.SessionID, Message: "too soon"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not awaiting a reply")
}

func TestIdeationTurn_RunnerError(t *testing.T) {
	runner := &fakeRunner{errs: []error{ideationErr("boom")}}
	creator := newFakeCreator()
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.NotEmpty(t, st.Error)
	assert.Nil(t, creator.capturedIssue())
}

type ideationErr string

func (a ideationErr) Error() string { return string(a) }

func TestIdeationConfident_CreatesTicket(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := newFakeCreator()
	var notified int
	e := newTestEngine(runner, creator, "PRJ", func() { notified++ })
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationDone)
	assert.Equal(t, "SC-999", st.CreatedKey)
	assert.Equal(t, 1, notified)

	issue := creator.capturedIssue()
	require.NotNil(t, issue)
	assert.Equal(t, "PRJ", issue.Project)
	assert.Equal(t, "T", issue.Title)
	assert.Equal(t, "D", issue.Description)

	for _, m := range st.Transcript {
		assert.NotContains(t, m.Text, ideationTicketMarker)
	}
}

func TestIdeationConfident_NoResolver(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	e := &IdeationEngine{Runner: runner, TurnTimeout: time.Second}
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.Contains(t, st.Error, "no PM ticket creator configured")
}

func TestIdeationConfident_ResolveCreatorError(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	e := &IdeationEngine{
		Runner: runner,
		ResolveCreator: func() (tracker.Creator, string, error) {
			return nil, "", ideationErr("no PM tracker")
		},
		TurnTimeout: time.Second,
	}
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.Contains(t, st.Error, "no PM tracker")
}

func TestIdeationConfident_CreateFails(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := &fakeCreator{err: ideationErr("tracker down")}
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.Contains(t, st.Error, "creating PM ticket")
}

func TestIdeationConfident_MalformedBlockRepaired(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: "[human:ideation-ticket]\n```json\n{broken\n```", ResumeID: "cs-1"},
		{Reply: markerBlock("T", "D"), ResumeID: "cs-2"},
	}}
	creator := newFakeCreator()
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationDone)
	require.Equal(t, 2, runner.callCount())
	call := runner.callAt(1)
	assert.Equal(t, "cs-1", call.resumeID)
	assert.Equal(t, ideationRepairPrompt, call.prompt)

	for _, m := range st.Transcript {
		assert.NotContains(t, m.Text, "broken")
	}
	assert.NotNil(t, creator.capturedIssue())
}

func TestIdeationConfident_MalformedBlockTwice(t *testing.T) {
	broken := "[human:ideation-ticket]\n```json\n{broken\n```"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: broken, ResumeID: "cs-1"},
		{Reply: broken, ResumeID: "cs-2"},
	}}
	creator := newFakeCreator()
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.Equal(t, 2, runner.callCount())
	assert.Contains(t, st.Error, "malformed ticket block")

	found := false
	for _, m := range st.Transcript {
		if m.Text == broken {
			found = true
		}
	}
	assert.True(t, found, "raw malformed reply should be appended to transcript")
	assert.Nil(t, creator.capturedIssue())
}

func TestIdeationConfident_EmptyTitle(t *testing.T) {
	emptyTitle := markerBlock("", "D")
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: emptyTitle, ResumeID: "cs-1"},
		{Reply: emptyTitle, ResumeID: "cs-2"},
	}}
	creator := newFakeCreator()
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	waitForState(t, e, IdeationError)
	assert.Equal(t, 2, runner.callCount())
	assert.Nil(t, creator.capturedIssue())
}

func TestIdeationRepairBudget_ResetsPerRound(t *testing.T) {
	broken := "[human:ideation-ticket]\n```json\n{broken\n```"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: broken, ResumeID: "cs-1"},                // turn 1: malformed
		{Reply: "Q1?", ResumeID: "cs-2"},                 // repair turn: agent asks a question instead (no marker)
		{Reply: broken, ResumeID: "cs-3"},                // after user reply: malformed again
		{Reply: markerBlock("T", "D"), ResumeID: "cs-4"}, // its own repair turn succeeds
	}}
	creator := newFakeCreator()
	e := newTestEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationAwaitingReply)
	require.Equal(t, 2, runner.callCount())

	_, err = e.Reply(IdeationReplyRequest{SessionID: st.SessionID, Message: "answer"})
	require.NoError(t, err)

	waitForState(t, e, IdeationDone)
	assert.Equal(t, 4, runner.callCount())
	assert.NotNil(t, creator.capturedIssue())
}

func TestIdeationStatus_NoSession(t *testing.T) {
	e := &IdeationEngine{}
	st := e.Status()
	assert.Equal(t, IdeationNone, st.State)
}

func TestParseTicketBlock_Variants(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		found   bool
		wantErr bool
	}{
		{name: "no marker", reply: "just a question?", found: false},
		{name: "marker with json-tagged fence", reply: markerBlock("T", "D"), found: true},
		{
			name:  "marker with bare fence",
			reply: "[human:ideation-ticket]\n```\n{\"title\": \"T\", \"description\": \"D\"}\n```",
			found: true,
		},
		{
			name:  "preamble text before marker",
			reply: "Great, I'm confident now.\n" + markerBlock("T", "D"),
			found: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, found, err := parseTicketBlock(tc.reply)
			assert.Equal(t, tc.found, found)
			if tc.wantErr {
				assert.Error(t, err)
			} else if tc.found {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIdeationGuidedQuestionParsed(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: questionBlock("Who has this pain?", "content", "PM users", "Engineers"), ResumeID: "cs-1"},
	}}
	e := newTestGuidedEngine(runner, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationAwaitingReply)
	require.NotNil(t, st.Question)
	assert.Equal(t, "Who has this pain?", st.Question.Text)
	assert.Equal(t, []string{"PM users", "Engineers"}, st.Question.Options)
	assert.Equal(t, "content", st.Question.Kind)
	assert.Equal(t, IdeationModeGuided, st.Mode)
}

func TestIdeationGuidedQuestionMalformed_Repaired(t *testing.T) {
	broken := "[human:ideation-question]\n```json\n{broken\n```"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: broken, ResumeID: "cs-1"},
		{Reply: questionBlock("Who has this pain?", "content", "PM users", "Engineers"), ResumeID: "cs-2"},
	}}
	e := newTestGuidedEngine(runner, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationAwaitingReply)
	require.Equal(t, 2, runner.callCount())
	call := runner.callAt(1)
	assert.Equal(t, "cs-1", call.resumeID)
	assert.Equal(t, ideationQuestionRepairPrompt, call.prompt)
	require.NotNil(t, st.Question)
	assert.Equal(t, "Who has this pain?", st.Question.Text)
}

func TestIdeationGuidedQuestionMalformed_RepairExhausted(t *testing.T) {
	broken := "[human:ideation-question]\n```json\n{broken\n```"
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: broken, ResumeID: "cs-1"},
		{Reply: broken, ResumeID: "cs-2"},
	}}
	e := newTestGuidedEngine(runner, newFakeCreator(), "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationError)
	assert.Equal(t, 2, runner.callCount())
	assert.Contains(t, st.Error, "malformed question block")
}

func TestIdeationGuidedTicketBlock_AwaitsApproval(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := newFakeCreator()
	e := newTestGuidedEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationAwaitingApproval)
	require.NotNil(t, st.Draft)
	assert.Equal(t, "T", st.Draft.Title)
	assert.Equal(t, "D", st.Draft.Description)
	assert.Nil(t, creator.capturedIssue())
}

func TestIdeationApprove_CreatesWithEditedFields(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := newFakeCreator()
	e := newTestGuidedEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)
	st := waitForState(t, e, IdeationAwaitingApproval)

	approved, err := e.Approve(IdeationApproveRequest{SessionID: st.SessionID, Title: "edited title", Description: "edited desc"})
	require.NoError(t, err)
	assert.Equal(t, IdeationDone, approved.State)
	assert.Equal(t, "SC-999", approved.CreatedKey)

	issue := creator.capturedIssue()
	require.NotNil(t, issue)
	assert.Equal(t, "edited title", issue.Title)
	assert.Equal(t, "edited desc", issue.Description)
}

func TestIdeationApprove_WrongSession(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := newFakeCreator()
	e := newTestGuidedEngine(runner, creator, "PRJ", nil)
	_, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)
	waitForState(t, e, IdeationAwaitingApproval)

	_, err = e.Approve(IdeationApproveRequest{SessionID: "nope", Title: "T"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching ideation session")

	st := e.Status()
	assert.Equal(t, IdeationAwaitingApproval, st.State)
}

func TestIdeationApprove_WrongState(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	runner := &blockingRunner{release: block}
	e := newTestGuidedEngine(runner, newFakeCreator(), "PRJ", nil)
	st, err := e.Start(IdeationStartRequest{Seed: "seed", Mode: IdeationModeGuided})
	require.NoError(t, err)
	require.Equal(t, IdeationThinking, st.State)

	_, err = e.Approve(IdeationApproveRequest{SessionID: st.SessionID, Title: "T"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not awaiting approval")
}

func TestIdeationChatMode_Unchanged(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{
		{Reply: markerBlock("T", "D"), ResumeID: "cs-1"},
	}}
	creator := newFakeCreator()
	e := newTestGuidedEngine(runner, creator, "PRJ", nil)
	// Mode left unset: defaults to chat.
	_, err := e.Start(IdeationStartRequest{Seed: "seed"})
	require.NoError(t, err)

	st := waitForState(t, e, IdeationDone)
	assert.Equal(t, "SC-999", st.CreatedKey)
	assert.NotNil(t, creator.capturedIssue())
}
