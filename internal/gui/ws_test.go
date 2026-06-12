package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/daemon"
)

// dialWS connects an authenticated WebSocket client to a test server.
func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	header := http.Header{"Authorization": {"Bearer secret-token"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readMessage(t *testing.T, conn *websocket.Conn) wsMessage {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var msg wsMessage
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &msg))
	return msg
}

func TestWS_RequiresAuth(t *testing.T) {
	s := testServer()
	s.AttachHub(NewHub(nil))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWS_BroadcastReachesClient(t *testing.T) {
	s := testServer()
	hub := NewHub(nil)
	s.AttachHub(hub)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	conn := dialWS(t, ts)

	// The client registers asynchronously after the upgrade; retry the
	// broadcast until it lands.
	require.Eventually(t, func() bool {
		hub.Broadcast(wsMessage{Type: "snapshot", Data: SnapshotDTO{Hostname: "h"}})
		hub.mu.Lock()
		n := len(hub.clients)
		hub.mu.Unlock()
		return n == 1
	}, 2*time.Second, 10*time.Millisecond)

	msg := readMessage(t, conn)
	assert.Equal(t, "snapshot", msg.Type)
}

func TestWS_HookBridgeForwardsAgentEvents(t *testing.T) {
	s := testServer()
	store := daemon.NewHookEventStore()
	s.Hooks = store
	s.Confirms = daemon.NewPendingConfirmStore()
	hub := NewHub(nil)
	s.AttachHub(hub)

	done := make(chan struct{})
	defer close(done)
	go s.runHookBridge(hub, done)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	conn := dialWS(t, ts)

	// Wait until the client is attached, then emit the lifecycle event.
	require.Eventually(t, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.clients) == 1
	}, 2*time.Second, 10*time.Millisecond)

	store.Append(hookevents.Event{EventName: "AgentStopped", AgentName: "agent-7", Timestamp: time.Now()})

	msg := readMessage(t, conn)
	assert.Equal(t, "agent-stopped", msg.Type)
	data, err := json.Marshal(msg.Data)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"agent-7"}`, string(data))

	// The bridge also pushes the pending-confirm set on hook activity.
	msg = readMessage(t, conn)
	assert.Equal(t, "confirms", msg.Type)
}

func TestWS_HookBridgeForwardsDispatchStatus(t *testing.T) {
	s := testServer()
	store := daemon.NewHookEventStore()
	s.Hooks = store
	hub := NewHub(nil)
	s.AttachHub(hub)

	done := make(chan struct{})
	defer close(done)
	go s.runHookBridge(hub, done)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	conn := dialWS(t, ts)

	require.Eventually(t, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.clients) == 1
	}, 2*time.Second, 10*time.Millisecond)

	store.Append(hookevents.Event{EventName: "AgentStartFailed", AgentName: "agent-9", Timestamp: time.Now()})

	msg := readMessage(t, conn)
	assert.Equal(t, "dispatch-status", msg.Type)
	data, err := json.Marshal(msg.Data)
	require.NoError(t, err)
	assert.Contains(t, string(data), "agent-9")
}

func TestHub_PollerBroadcastWiring(t *testing.T) {
	p := NewPoller(&fakeFetcher{})
	hub := NewHub(p)

	ch := hub.attach()
	defer hub.detach(ch)

	// NewHub wires the poller's broadcast to a "snapshot" push.
	p.broadcast(SnapshotDTO{Hostname: "wired"})

	select {
	case msg := <-ch:
		assert.Equal(t, "snapshot", msg.Type)
	case <-time.After(time.Second):
		t.Fatal("expected a snapshot push")
	}
}

func TestHub_SlowClientDoesNotBlockBroadcast(t *testing.T) {
	hub := NewHub(nil)
	ch := hub.attach()
	defer hub.detach(ch)

	// Saturate the client buffer; further broadcasts must not block.
	for i := 0; i < wsSendBuffer+5; i++ {
		hub.Broadcast(wsMessage{Type: "snapshot"})
	}
}
