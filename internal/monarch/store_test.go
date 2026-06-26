package monarch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDBPath(t *testing.T) {
	assert.True(t, strings.HasSuffix(DefaultDBPath(), "monarch.db"))
}

func TestNewStore_filePathRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monarch.db")
	s, err := NewStore(path)
	require.NoError(t, err)

	now := time.Now().UTC()
	insert(t, s, Event{Type: EventAgentStart, DaemonID: "d1", AgentID: "a1", Repo: "cli", State: StateCoding, TS: now})
	require.NoError(t, s.Close())

	// Reopen the same file: the row must survive.
	s2, err := NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	board, err := s2.WorkBoard(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, board, 1)
	assert.Equal(t, "d1", board[0].DaemonID)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insert(t *testing.T, s *Store, e Event) {
	t.Helper()
	require.NoError(t, s.Insert(context.Background(), e))
}

func TestNewStore_errorWhenDirUncreatable(t *testing.T) {
	// A regular file standing where a directory should be makes MkdirAll fail,
	// exercising NewStore's directory-creation error path.
	file := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err := NewStore(filepath.Join(file, "monarch.db"))
	require.Error(t, err)
}

func TestStore_Insert_WorkBoard(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	insert(t, s, Event{Type: EventAgentStart, DaemonID: "daemon-1", AgentID: "agent-a", Repo: "cli", State: StateCoding, TS: now.Add(-2 * time.Minute)})
	insert(t, s, Event{Type: EventAgentStart, DaemonID: "daemon-1", AgentID: "agent-a", Repo: "cli", State: StateBlocked, TS: now.Add(-1 * time.Minute)})

	board, err := s.WorkBoard(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, board, 1)
	assert.Equal(t, "blocked", board[0].State)
	assert.Equal(t, "cli", board[0].Repo)
}

// A heartbeat keeps an idle daemon visible in capacity without ever appearing as
// a (blank) row on the work board.
func TestStore_Heartbeat_presenceNotWork(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	insert(t, s, Event{Type: EventHeartbeat, DaemonID: "daemon-idle", State: StateIdle, TS: now.Add(-5 * time.Second)})

	board, err := s.WorkBoard(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, board, "heartbeats must not show on the work board")

	cap, err := s.Capacity(context.Background(), now.Add(-time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, cap.Daemons, "heartbeating daemon counts as connected")
	assert.Equal(t, 1, cap.Idle)
	assert.Equal(t, 0, cap.Busy)
}

func TestStore_WorkBoard_excludesStopped(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	insert(t, s, Event{Type: EventAgentStart, DaemonID: "daemon-1", AgentID: "agent-a", State: StateCoding, TS: now.Add(-2 * time.Minute)})
	insert(t, s, Event{Type: EventAgentStop, DaemonID: "daemon-1", AgentID: "agent-a", State: StateStopped, TS: now.Add(-1 * time.Minute)})

	board, err := s.WorkBoard(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, board)
}

func TestStore_BurnByTicket_sumsLatestPerSession(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	insert(t, s, Event{Type: EventTokensUsed, DaemonID: "d", AgentID: "agent-a", TicketKey: "HUM-1", TS: now.Add(-10 * time.Minute), Payload: &TokenPayload{InputTokens: 100}})
	insert(t, s, Event{Type: EventTokensUsed, DaemonID: "d", AgentID: "agent-a", TicketKey: "HUM-1", TS: now.Add(-1 * time.Minute), Payload: &TokenPayload{InputTokens: 250}})

	rows, err := s.BurnByTicket(context.Background(), dayStart)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "HUM-1", rows[0].Key)
	assert.Equal(t, 250, rows[0].InputTokens, "latest cumulative wins, not the sum")
}

func TestStore_BurnByTicket_repoFallback(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	insert(t, s, Event{Type: EventTokensUsed, DaemonID: "d", AgentID: "agent-a", Repo: "cli", TS: now.Add(-1 * time.Minute), Payload: &TokenPayload{OutputTokens: 5}})

	rows, err := s.BurnByTicket(context.Background(), dayStart)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "cli", rows[0].Key)
	assert.Equal(t, 5, rows[0].OutputTokens)
}

func TestStore_BurnByRepo(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	insert(t, s, Event{Type: EventTokensUsed, DaemonID: "d", AgentID: "agent-a", Repo: "cli", TicketKey: "HUM-1", TS: now.Add(-1 * time.Minute), Payload: &TokenPayload{InputTokens: 30}})
	insert(t, s, Event{Type: EventTokensUsed, DaemonID: "d", AgentID: "agent-b", Repo: "cli", TicketKey: "HUM-2", TS: now.Add(-1 * time.Minute), Payload: &TokenPayload{InputTokens: 70}})

	rows, err := s.BurnByRepo(context.Background(), dayStart)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "cli", rows[0].Key)
	assert.Equal(t, 100, rows[0].InputTokens)
}

func TestStore_Capacity_classifies(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	insert(t, s, Event{Type: EventAgentStart, DaemonID: "d1", AgentID: "a1", State: StateCoding, TS: now.Add(-1 * time.Minute)})
	insert(t, s, Event{Type: EventAgentStart, DaemonID: "d2", AgentID: "a2", State: StateBlocked, TS: now.Add(-1 * time.Minute)})
	insert(t, s, Event{Type: EventAgentStart, DaemonID: "d3", AgentID: "a3", State: StatePlanning, TS: now.Add(-3 * time.Minute)})
	insert(t, s, Event{Type: EventAgentStop, DaemonID: "d3", AgentID: "a3", State: StateStopped, TS: now.Add(-1 * time.Minute)})

	cap, err := s.Capacity(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, cap.Daemons, "stopped daemon excluded")
	assert.Equal(t, 1, cap.Busy)
	assert.Equal(t, 1, cap.Blocked)
	assert.Equal(t, 0, cap.Idle)
}

func TestStore_Prune(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	insert(t, s, Event{Type: EventAgentStart, DaemonID: "old", AgentID: "a", State: StateCoding, TS: now.Add(-(RetentionDays + 1) * 24 * time.Hour)})
	insert(t, s, Event{Type: EventAgentStart, DaemonID: "new", AgentID: "b", State: StateCoding, TS: now.Add(-1 * time.Minute)})

	deleted, err := s.Prune(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	board, err := s.WorkBoard(context.Background(), now.Add(-2*RetentionDays*24*time.Hour))
	require.NoError(t, err)
	require.Len(t, board, 1)
	assert.Equal(t, "new", board[0].DaemonID)
}
