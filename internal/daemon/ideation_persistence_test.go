package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- store implementation ---

func TestIdeationDBRoundTrip(t *testing.T) {
	db, err := NewIdeationDB(":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Nothing stored yet.
	got, err := db.Load()
	require.NoError(t, err)
	assert.Nil(t, got)

	p := PersistedIdeation{
		ID:         "ideation-1",
		Mode:       IdeationModeGuided,
		State:      IdeationAwaitingReply,
		Transcript: []IdeationMessage{{Role: "user", Text: "an idea", Time: time.Now().UTC().Truncate(time.Second)}},
		ResumeID:   "resume-abc",
		Question:   &IdeationQuestion{Text: "who for?", Options: []string{"a", "b"}, Kind: "content"},
		UpdatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	require.NoError(t, db.Save(p))

	got, err = db.Load()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "ideation-1", got.ID)
	assert.Equal(t, IdeationAwaitingReply, got.State)
	assert.Equal(t, "resume-abc", got.ResumeID, "resume id must survive: it is what resumes the agent conversation")
	require.NotNil(t, got.Question)
	assert.Equal(t, "who for?", got.Question.Text)
	require.Len(t, got.Transcript, 1)
	assert.Equal(t, "an idea", got.Transcript[0].Text)

	require.NoError(t, db.Clear())
	got, err = db.Load()
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestIdeationDBKeepsOneRow proves repeated saves replace rather than
// accumulate — the engine owns exactly one session.
func TestIdeationDBKeepsOneRow(t *testing.T) {
	db, err := NewIdeationDB(":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	for i, state := range []IdeationState{IdeationThinking, IdeationAwaitingReply, IdeationAwaitingApproval} {
		require.NoError(t, db.Save(PersistedIdeation{ID: "s", State: state, UpdatedAt: time.Now()}), "save %d", i)
	}
	var rows int
	require.NoError(t, db.db.QueryRow(`SELECT COUNT(*) FROM ideation_session`).Scan(&rows))
	assert.Equal(t, 1, rows)

	got, err := db.Load()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, IdeationAwaitingApproval, got.State, "last save wins")
}

// --- restore policy ---

func TestPersistedIdeationRestorable(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Minute)
	stale := now.Add(-48 * time.Hour)

	cases := []struct {
		name string
		p    PersistedIdeation
		want bool
	}{
		{"live awaiting reply", PersistedIdeation{ID: "a", State: IdeationAwaitingReply, UpdatedAt: fresh}, true},
		{"live awaiting approval", PersistedIdeation{ID: "a", State: IdeationAwaitingApproval, UpdatedAt: fresh}, true},
		{"interrupted mid-turn", PersistedIdeation{ID: "a", State: IdeationThinking, UpdatedAt: fresh}, true},
		{"finished", PersistedIdeation{ID: "a", State: IdeationDone, UpdatedAt: fresh}, false},
		{"errored", PersistedIdeation{ID: "a", State: IdeationError, UpdatedAt: fresh}, false},
		{"empty", PersistedIdeation{ID: "", State: IdeationAwaitingReply, UpdatedAt: fresh}, false},
		{"stale", PersistedIdeation{ID: "a", State: IdeationAwaitingReply, UpdatedAt: stale}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.restorable(now, IdeationMaxAge))
		})
	}
}

// TestNormalizeForRestoreThinking proves a session saved mid-turn cannot come
// back as a spinner that hangs forever: the goroutine running that turn died
// with the old process, so nothing would ever complete it.
func TestNormalizeForRestoreThinking(t *testing.T) {
	got := normalizeForRestore(PersistedIdeation{ID: "a", State: IdeationThinking})
	assert.Equal(t, IdeationError, got.State)
	assert.NotEmpty(t, got.ErrMsg)

	// Other states pass through untouched.
	awaiting := PersistedIdeation{ID: "a", State: IdeationAwaitingReply}
	assert.Equal(t, awaiting, normalizeForRestore(awaiting))
}

// --- engine integration ---

// memIdeationStore is an in-memory IdeationStore that records every save.
type memIdeationStore struct {
	mu     sync.Mutex
	cur    *PersistedIdeation
	saves  int
	clears int
}

func (m *memIdeationStore) Save(p PersistedIdeation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := p
	m.cur = &cp
	m.saves++
	return nil
}

func (m *memIdeationStore) Load() (*PersistedIdeation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, nil
}

func (m *memIdeationStore) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = nil
	m.clears++
	return nil
}

func (m *memIdeationStore) snapshot() (*PersistedIdeation, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, m.saves
}

// TestEnginePersistsAcrossTurns proves the state a restart would need is
// written at each step of a live chat, not just at the end.
func TestEnginePersistsAcrossTurns(t *testing.T) {
	store := &memIdeationStore{}
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "what problem?", ResumeID: "r1"}}}
	e := &IdeationEngine{Runner: runner, TurnTimeout: 2 * time.Second, Store: store}

	_, err := e.Start(IdeationStartRequest{Seed: "an idea"})
	require.NoError(t, err)

	// The turn completes asynchronously; wait for the awaiting-reply state.
	require.Eventually(t, func() bool {
		return e.Status().State == IdeationAwaitingReply
	}, 2*time.Second, 10*time.Millisecond)

	saved, saves := store.snapshot()
	require.NotNil(t, saved)
	assert.Positive(t, saves, "a live chat must be written through as it progresses")
	assert.Equal(t, IdeationAwaitingReply, saved.State)
	assert.Equal(t, "r1", saved.ResumeID, "the resume id is what lets a restored chat continue")
	assert.NotEmpty(t, saved.Transcript)
}

// TestEngineRestoreResumesChat is the gap this closes: a daemon restart that
// lands between turns brings the conversation back instead of resetting it.
func TestEngineRestoreResumesChat(t *testing.T) {
	store := &memIdeationStore{}
	e := &IdeationEngine{Runner: &fakeRunner{}, TurnTimeout: time.Second, Store: store}

	saved := PersistedIdeation{
		ID:         "ideation-42",
		Mode:       IdeationModeChat,
		State:      IdeationAwaitingReply,
		ResumeID:   "resume-xyz",
		Transcript: []IdeationMessage{{Role: "user", Text: "an idea"}, {Role: "agent", Text: "what problem?"}},
		UpdatedAt:  time.Now(),
	}
	require.True(t, e.Restore(saved, time.Now(), IdeationMaxAge))

	st := e.Status()
	assert.Equal(t, "ideation-42", st.SessionID)
	assert.Equal(t, IdeationAwaitingReply, st.State)
	assert.Len(t, st.Transcript, 2)

	// The restored session is live: replying continues it on the saved
	// provider conversation rather than starting a new one.
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "got it", ResumeID: "resume-xyz2"}}}
	e.Runner = runner
	_, err := e.Reply(IdeationReplyRequest{SessionID: "ideation-42", Message: "for developers"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return e.Status().State == IdeationAwaitingReply
	}, 2*time.Second, 10*time.Millisecond)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	require.NotEmpty(t, runner.calls)
	assert.Equal(t, "resume-xyz", runner.calls[0].resumeID,
		"the restored chat must resume the provider session, not start fresh")
}

func TestEngineRestoreSkipsFinishedAndStale(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		p    PersistedIdeation
	}{
		{"finished", PersistedIdeation{ID: "a", State: IdeationDone, UpdatedAt: now}},
		{"stale", PersistedIdeation{ID: "a", State: IdeationAwaitingReply, UpdatedAt: now.Add(-48 * time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &IdeationEngine{Runner: &fakeRunner{}}
			assert.False(t, e.Restore(tc.p, now, IdeationMaxAge))
			assert.Equal(t, IdeationNone, e.Status().State)
		})
	}
}

// TestEngineWithoutStoreStillRuns proves persistence is optional — a failed
// database open degrades to memory-only instead of breaking ideation.
func TestEngineWithoutStoreStillRuns(t *testing.T) {
	runner := &fakeRunner{turns: []IdeationTurn{{Reply: "what problem?", ResumeID: "r1"}}}
	e := &IdeationEngine{Runner: runner, TurnTimeout: 2 * time.Second} // Store nil

	_, err := e.Start(IdeationStartRequest{Seed: "an idea"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return e.Status().State == IdeationAwaitingReply
	}, 2*time.Second, 10*time.Millisecond)
}
