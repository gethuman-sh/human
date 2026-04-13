package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/StephanSchmidt/human/internal/claude"
	"github.com/StephanSchmidt/human/internal/claude/hookevents"
	"github.com/StephanSchmidt/human/internal/claude/logparser"
)

// --- stubs ---

type stubFinder struct {
	instances []claude.Instance
	err       error
}

func (s *stubFinder) FindInstances(_ context.Context) ([]claude.Instance, error) {
	return s.instances, s.err
}

// --- overlayHookState tests ---

func TestOverlayHookState_NoHooks(t *testing.T) {
	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusWorking},
	}
	overlayHookState(byPath, nil)
	assert.Equal(t, logparser.StatusWorking, byPath["/a.jsonl"].Status, "should remain unchanged")
}

func TestOverlayHookState_HookNewer(t *testing.T) {
	jsonlTime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	hookTime := time.Date(2026, 3, 25, 10, 0, 5, 0, time.UTC)

	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusWorking, LastActivity: jsonlTime},
	}
	hooks := map[string]hookevents.SessionSnapshot{
		"s1": {SessionID: "s1", Status: logparser.StatusReady, LastEventAt: hookTime},
	}
	overlayHookState(byPath, hooks)

	sess := byPath["/a.jsonl"]
	assert.Equal(t, logparser.StatusReady, sess.Status, "hook says idle")
	assert.Equal(t, hookTime, sess.LastActivity)
}

func TestOverlayHookState_BlockedAndError(t *testing.T) {
	jsonlTime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	hookTime := time.Date(2026, 3, 25, 10, 0, 5, 0, time.UTC)

	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusWorking, LastActivity: jsonlTime},
		"/b.jsonl": {SessionID: "s2", Status: logparser.StatusWorking, LastActivity: jsonlTime},
	}
	hooks := map[string]hookevents.SessionSnapshot{
		"s1": {SessionID: "s1", Status: logparser.StatusBlocked, LastEventAt: hookTime},
		"s2": {SessionID: "s2", Status: logparser.StatusError, LastEventAt: hookTime},
	}
	overlayHookState(byPath, hooks)

	assert.Equal(t, logparser.StatusBlocked, byPath["/a.jsonl"].Status)
	assert.Equal(t, logparser.StatusError, byPath["/b.jsonl"].Status)
}

func TestOverlayHookState_SkipsStaleHook(t *testing.T) {
	jsonlTime := time.Date(2026, 3, 25, 10, 0, 5, 0, time.UTC)
	hookTime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusReady, LastActivity: jsonlTime},
	}
	hooks := map[string]hookevents.SessionSnapshot{
		"s1": {SessionID: "s1", Status: logparser.StatusBlocked, BlockedTool: "Bash", LastEventAt: hookTime},
	}
	overlayHookState(byPath, hooks)

	sess := byPath["/a.jsonl"]
	assert.Equal(t, logparser.StatusReady, sess.Status, "stale hook should not override newer JSONL state")
	assert.Empty(t, sess.BlockedTool, "stale hook fields should not be applied")
	assert.Equal(t, jsonlTime, sess.LastActivity, "LastActivity should keep JSONL timestamp")
}

// --- fillMissingFromHooks tests ---

func TestFillMissingFromHooks_MatchesByCwd(t *testing.T) {
	hookTime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)

	instances := []claude.Instance{
		{FilePath: "/a.jsonl", Cwd: "/home/user/project"},
	}
	byPath := map[string]logparser.SessionState{} // no JSONL session
	hooks := map[string]hookevents.SessionSnapshot{
		"s-new": {SessionID: "s-new", Cwd: "/home/user/project", Status: logparser.StatusReady, LastEventAt: hookTime},
	}

	fillMissingFromHooks(instances, byPath, hooks)

	require.Contains(t, byPath, "/a.jsonl")
	sess := byPath["/a.jsonl"]
	assert.Equal(t, "s-new", sess.SessionID)
	assert.Equal(t, logparser.StatusReady, sess.Status)
	assert.Equal(t, "/home/user/project", sess.Cwd)
}

func TestFillMissingFromHooks_SkipsAlreadyMatched(t *testing.T) {
	instances := []claude.Instance{
		{FilePath: "/a.jsonl", Cwd: "/home/user/project"},
	}
	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s-old", Status: logparser.StatusWorking},
	}
	hooks := map[string]hookevents.SessionSnapshot{
		"s-new": {SessionID: "s-new", Cwd: "/home/user/project", Status: logparser.StatusReady},
	}

	fillMissingFromHooks(instances, byPath, hooks)

	assert.Equal(t, "s-old", byPath["/a.jsonl"].SessionID, "existing session should not be overwritten")
	assert.Equal(t, logparser.StatusWorking, byPath["/a.jsonl"].Status)
}

func TestFillMissingFromHooks_NoHooks(t *testing.T) {
	instances := []claude.Instance{{FilePath: "/a.jsonl", Cwd: "/proj"}}
	byPath := map[string]logparser.SessionState{}

	fillMissingFromHooks(instances, byPath, nil)
	assert.Empty(t, byPath)
}

func TestFillMissingFromHooks_NoCwd(t *testing.T) {
	instances := []claude.Instance{{FilePath: "/a.jsonl"}} // no Cwd
	byPath := map[string]logparser.SessionState{}
	hooks := map[string]hookevents.SessionSnapshot{
		"s1": {SessionID: "s1", Cwd: "/proj", Status: logparser.StatusReady},
	}

	fillMissingFromHooks(instances, byPath, hooks)
	assert.Empty(t, byPath, "instance without cwd should not match")
}

func TestFillMissingFromHooks_ContainerByRoot(t *testing.T) {
	hookTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)

	instances := []claude.Instance{
		{Source: "container", Root: "/container/abc123", Cwd: "/workspaces/cli"},
	}
	byPath := map[string]logparser.SessionState{}
	hooks := map[string]hookevents.SessionSnapshot{
		"s-container": {SessionID: "s-container", Cwd: "/workspaces/cli", Status: logparser.StatusWorking, LastEventAt: hookTime},
	}

	fillMissingFromHooks(instances, byPath, hooks)

	require.Contains(t, byPath, "/container/abc123")
	sess := byPath["/container/abc123"]
	assert.Equal(t, "s-container", sess.SessionID)
	assert.Equal(t, logparser.StatusWorking, sess.Status)
}

// --- matchInstances tests ---

func TestMatchInstances_ContainerByRoot(t *testing.T) {
	usages := []claude.InstanceUsage{
		{Instance: claude.Instance{Source: "container", Root: "/container/abc123"}},
	}
	byPath := map[string]logparser.SessionState{
		"/container/abc123": {SessionID: "s1", Status: logparser.StatusWorking},
	}
	views := matchInstances(usages, byPath)
	require.Len(t, views, 1)
	require.NotNil(t, views[0].Session)
	assert.Equal(t, "s1", views[0].Session.SessionID)
	assert.Equal(t, logparser.StatusWorking, views[0].Session.Status)
}

func TestMatchInstances_WithSession(t *testing.T) {
	usages := []claude.InstanceUsage{
		{Instance: claude.Instance{FilePath: "/a.jsonl"}},
	}
	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusWorking},
	}
	views := matchInstances(usages, byPath)
	require.Len(t, views, 1)
	require.NotNil(t, views[0].Session)
	assert.Equal(t, "s1", views[0].Session.SessionID)
}

func TestMatchInstances_NoSession(t *testing.T) {
	usages := []claude.InstanceUsage{
		{Instance: claude.Instance{FilePath: "/a.jsonl"}},
	}
	views := matchInstances(usages, nil)
	require.Len(t, views, 1)
	assert.Nil(t, views[0].Session)
}

// --- matchPaneStates tests ---

func TestMatchPaneStates_ByPID(t *testing.T) {
	panes := []claude.TmuxPane{{ClaudePID: 100}}
	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Status: logparser.StatusWorking},
	}
	instances := []claude.Instance{{PID: 100, FilePath: "/a.jsonl"}}

	matchPaneStates(panes, byPath, instances)
	assert.Equal(t, claude.StateBusy, panes[0].State)
}

func TestMatchPaneStates_ByCwd(t *testing.T) {
	panes := []claude.TmuxPane{{Cwd: "/proj"}}
	byPath := map[string]logparser.SessionState{
		"/a.jsonl": {SessionID: "s1", Cwd: "/proj", Status: logparser.StatusReady, LastActivity: time.Now()},
	}

	matchPaneStates(panes, byPath, nil)
	assert.Equal(t, claude.StateReady, panes[0].State)
}

func TestMatchPaneStates_NoMatch(t *testing.T) {
	panes := []claude.TmuxPane{{ClaudePID: 999}}
	matchPaneStates(panes, nil, nil)
	assert.Equal(t, claude.StateUnknown, panes[0].State)
}

// --- collectContainerIDs tests ---

func TestCollectContainerIDs(t *testing.T) {
	instances := []claude.Instance{
		{Source: "host", ContainerID: ""},
		{Source: "container", ContainerID: "abc123"},
		{Source: "container", ContainerID: "def456"},
	}
	ids := collectContainerIDs(instances)
	assert.Equal(t, []string{"abc123", "def456"}, ids)
}

func TestCollectContainerIDs_Empty(t *testing.T) {
	ids := collectContainerIDs(nil)
	assert.Nil(t, ids)
}

// --- extractUsages tests ---

func TestExtractUsages(t *testing.T) {
	views := []InstanceView{
		{Usage: claude.InstanceUsage{Instance: claude.Instance{Label: "a"}}},
		{Usage: claude.InstanceUsage{Instance: claude.Instance{Label: "b"}}},
	}
	usages := extractUsages(views)
	require.Len(t, usages, 2)
	assert.Equal(t, "a", usages[0].Instance.Label)
	assert.Equal(t, "b", usages[1].Instance.Label)
}

// --- aggregateUsage tests ---

func TestAggregateUsage(t *testing.T) {
	usages := []claude.InstanceUsage{
		{Summary: &claude.UsageSummary{Models: map[string]*claude.ModelUsage{
			"opus": {InputTokens: 100, OutputTokens: 50},
		}}},
		{Summary: &claude.UsageSummary{Models: map[string]*claude.ModelUsage{
			"opus": {InputTokens: 200, OutputTokens: 100},
		}}},
	}
	total := aggregateUsage(usages)
	require.NotNil(t, total.Models["opus"])
	assert.Equal(t, 300, total.Models["opus"].InputTokens)
	assert.Equal(t, 150, total.Models["opus"].OutputTokens)
}

// --- sessionToState tests ---

func TestSessionToState(t *testing.T) {
	assert.Equal(t, claude.StateBusy, sessionToState(logparser.SessionState{Status: logparser.StatusWorking}))
	assert.Equal(t, claude.StateReady, sessionToState(logparser.SessionState{Status: logparser.StatusReady}))
	assert.Equal(t, claude.StateBlocked, sessionToState(logparser.SessionState{Status: logparser.StatusBlocked}))
	assert.Equal(t, claude.StateWaiting, sessionToState(logparser.SessionState{Status: logparser.StatusWaiting}))
	assert.Equal(t, claude.StateError, sessionToState(logparser.SessionState{Status: logparser.StatusError}))
	assert.Equal(t, claude.StateReady, sessionToState(logparser.SessionState{Status: logparser.StatusEnded}))
}

// --- FetchFull integration test ---

func TestFetchFull_NoInstances(t *testing.T) {
	mon := New(&stubFinder{}, nil)
	snap := mon.FetchFull(context.Background())
	require.NotNil(t, snap)
	assert.NoError(t, snap.Err)
	assert.Empty(t, snap.Instances)
	require.NotNil(t, snap.TotalUsage)
}

func TestFetchFull_FinderError(t *testing.T) {
	mon := New(&stubFinder{err: context.DeadlineExceeded}, nil)
	snap := mon.FetchFull(context.Background())
	require.NotNil(t, snap)
	assert.ErrorIs(t, snap.Err, context.DeadlineExceeded)
}

// --- parseSessions tests ---

func TestParseSessions_ContainerInstance(t *testing.T) {
	mon := New(&stubFinder{}, nil)

	// Minimal JSONL that establishes a session.
	jsonl := []byte(`{"type":"system","sessionId":"sess-ctr","cwd":"/workspaces/cli"}` + "\n")

	instances := []claude.Instance{
		{Source: "container", Root: "/container/abc123", Walker: &claude.ByteWalker{Data: jsonl}},
	}
	byPath := mon.parseSessions(instances)

	require.Contains(t, byPath, "/container/abc123")
	assert.Equal(t, "sess-ctr", byPath["/container/abc123"].SessionID)
}

func TestParseSessions_PrunesStaleParser(t *testing.T) {
	// When an instance's FilePath changes (e.g. JSONL resolution corrects
	// after startup race), the old parser should be pruned.
	mon := New(&stubFinder{}, nil)

	// Simulate first cycle: instance resolves to old JSONL.
	mon.parsers["/old.jsonl"] = logparser.NewFileParser()

	// Second cycle: instance now resolves to correct JSONL.
	// parseSessions won't reference /old.jsonl anymore.
	instances := []claude.Instance{
		{FilePath: "/new.jsonl"},
	}
	mon.parseSessions(instances)

	assert.NotContains(t, mon.parsers, "/old.jsonl", "stale parser should be pruned")
	assert.Contains(t, mon.parsers, "/new.jsonl", "active parser should exist")
}

// --- extractInstances tests ---

func TestExtractInstances(t *testing.T) {
	views := []InstanceView{
		{Usage: claude.InstanceUsage{Instance: claude.Instance{Label: "a", PID: 1}}},
		{Usage: claude.InstanceUsage{Instance: claude.Instance{Label: "b", PID: 2}}},
	}
	instances := extractInstances(views)
	require.Len(t, instances, 2)
	assert.Equal(t, "a", instances[0].Label)
	assert.Equal(t, 2, instances[1].PID)
}
