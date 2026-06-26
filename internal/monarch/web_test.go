package monarch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWebTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newWebServer(store *Store) *WebServer {
	return &WebServer{Addr: "127.0.0.1:0", Store: store, Logger: zerolog.Nop()}
}

func TestBuildSnapshot_mapsFieldsAndFallbacks(t *testing.T) {
	now := time.Now().UTC()
	board := []WorkItem{
		{DaemonID: "daemon-1", TicketKey: "HUM-143", Repo: "cli", Branch: "main", State: "coding"},
		{DaemonID: "daemon-2", TicketKey: "", Repo: "cli", State: "idle"},
	}
	burnTicket := []BurnRow{{Key: "HUM-143", InputTokens: 1500}}
	burnRepo := []BurnRow{{Key: "cli", InputTokens: 1500}}
	cap := Capacity{Daemons: 2, Busy: 1, Idle: 1}

	snap := buildSnapshot(now, board, burnTicket, burnRepo, cap)

	assert.Equal(t, now, snap.GeneratedAt)
	assert.Equal(t, capView{Daemons: 2}, snap.Capacity)
	require.Len(t, snap.Board, 2)
	assert.Equal(t, "daemon-1", snap.Board[0].Daemon)
	assert.Equal(t, "HUM-143", snap.Board[0].Ticket)
	assert.Equal(t, "cli", snap.Board[0].Repo)
	// An absent ticket falls back to the em dash, matching the old TUI.
	assert.Equal(t, emDash, snap.Board[1].Ticket)

	require.Len(t, snap.BurnByTicket, 1)
	assert.Equal(t, "HUM-143", snap.BurnByTicket[0].Key)
	assert.Equal(t, 1500, snap.BurnByTicket[0].Tokens)
	assert.Equal(t, "1.5K", snap.BurnByTicket[0].Display)
}

func TestToBurnViews_zeroAndEmptyKey(t *testing.T) {
	views := toBurnViews([]BurnRow{{Key: ""}})
	require.Len(t, views, 1)
	// Zero burn renders as a dash, and an empty key falls back to a dash too.
	assert.Equal(t, emDash, views[0].Key)
	assert.Equal(t, emDash, views[0].Display)
	assert.Equal(t, 0, views[0].Tokens)
}

func TestBuildSnapshot_emptyYieldsNonNilSlices(t *testing.T) {
	snap := buildSnapshot(time.Now().UTC(), nil, nil, nil, Capacity{})
	// Non-nil empty slices marshal as [] rather than null, keeping the JS simple.
	assert.NotNil(t, snap.Board)
	assert.NotNil(t, snap.BurnByTicket)
	assert.NotNil(t, snap.BurnByRepo)
	assert.Empty(t, snap.Board)
}

// serve drives the embedded router with an in-process recorder, avoiding a real
// socket (and the nil-response source a raw http.Get would introduce).
func serve(t *testing.T, store *Store, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	newWebServer(store).handler().ServeHTTP(rec, req)
	return rec
}

func TestSnapshotEndpoint_returnsLiveData(t *testing.T) {
	store := newWebTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, store.Insert(context.Background(), Event{
		Type: EventAgentStart, DaemonID: "daemon-1", AgentID: "a1",
		TicketKey: "HUM-143", Repo: "cli", State: StateCoding, TS: now,
	}))

	rec := serve(t, store, "/api/snapshot")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var snap Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Len(t, snap.Board, 1)
	assert.Equal(t, "daemon-1", snap.Board[0].Daemon)
	assert.Equal(t, "HUM-143", snap.Board[0].Ticket)
	assert.Equal(t, 1, snap.Capacity.Daemons)
}

func TestSnapshotEndpoint_emptyStore(t *testing.T) {
	rec := serve(t, newWebTestStore(t), "/api/snapshot")
	require.Equal(t, http.StatusOK, rec.Code)

	var snap Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	assert.Empty(t, snap.Board)
	assert.Equal(t, 0, snap.Capacity.Daemons)
}

func TestWebServer_servesEmbeddedIndex(t *testing.T) {
	rec := serve(t, newWebTestStore(t), "/")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "monarch")
}

func TestWebServer_servesStaticAssets(t *testing.T) {
	store := newWebTestStore(t)
	for _, asset := range []string{"/app.js", "/style.css"} {
		rec := serve(t, store, asset)
		require.Equal(t, http.StatusOK, rec.Code, asset)
	}
}

// TestWebServer_listenAndServeShutsDownOnContext verifies the server binds, then
// drains cleanly when its context is cancelled (no hang, no error).
func TestWebServer_listenAndServeShutsDownOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newWebServer(newWebTestStore(t))
	w.Addr = "127.0.0.1:0"

	done := make(chan error, 1)
	go func() { done <- w.ListenAndServe(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("web server did not shut down after context cancel")
	}
}
