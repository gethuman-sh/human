package gui

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsWriteTimeout = 10 * time.Second
	wsPingInterval = 30 * time.Second
	// wsSendBuffer absorbs short bursts; a client that cannot drain it is
	// disconnected rather than allowed to block the broadcast path.
	wsSendBuffer = 8
)

// wsMessage is the uniform server→client push envelope.
type wsMessage struct {
	Type string `json:"type"` // "snapshot" | "agent-stopped" | "confirms"
	Data any    `json:"data,omitempty"`
}

// agentStoppedData is the payload of an "agent-stopped" push.
type agentStoppedData struct {
	Name string `json:"name"`
}

// dispatchStatusData is the payload of a "dispatch-status" push — the
// GUI equivalent of the TUI's footer flash message.
type dispatchStatusData struct {
	Message string `json:"message"`
}

// Hub fans pushed messages out to connected WebSocket clients and bridges
// the daemon's HookEventStore change signal into push messages — the same
// source the TCP "subscribe" route uses, but in-process.
type Hub struct {
	poller *Poller

	mu      sync.Mutex
	clients map[chan wsMessage]struct{}
}

// NewHub creates a hub wired to the poller's broadcast callback.
func NewHub(poller *Poller) *Hub {
	h := &Hub{
		poller:  poller,
		clients: make(map[chan wsMessage]struct{}),
	}
	if poller != nil {
		poller.SetBroadcast(func(dto SnapshotDTO) {
			h.Broadcast(wsMessage{Type: "snapshot", Data: dto})
		})
	}
	return h
}

// Broadcast queues msg for every connected client. Slow clients are
// skipped; their connection is reaped by the write loop timeout.
func (h *Hub) Broadcast(msg wsMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (h *Hub) attach() chan wsMessage {
	ch := make(chan wsMessage, wsSendBuffer)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	if h.poller != nil {
		h.poller.ClientAttached()
	}
	return ch
}

func (h *Hub) detach(ch chan wsMessage) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	if h.poller != nil {
		h.poller.ClientDetached()
	}
}

// RunHookBridge converts hook-store change signals into push messages
// until the channel is closed via Unsubscribe. Mirrors the daemon's
// handleSubscribe loop: a sequence cursor (not slice length) tracks
// delivery so a saturated event ring cannot stall notifications.
func (s *Server) runHookBridge(hub *Hub, done <-chan struct{}) {
	if s.Hooks == nil {
		return
	}
	ch := s.Hooks.Subscribe()
	defer s.Hooks.Unsubscribe(ch)

	var lastSeq uint64
	for {
		select {
		case <-done:
			return
		case <-ch:
		}
		newEvents, seq := s.Hooks.EventsSince(lastSeq)
		lastSeq = seq
		for i := range newEvents {
			if newEvents[i].AgentName == "" {
				continue
			}
			switch newEvents[i].EventName {
			case "AgentStopped":
				hub.Broadcast(wsMessage{Type: "agent-stopped", Data: agentStoppedData{Name: newEvents[i].AgentName}})
			case "AgentStarted":
				hub.Broadcast(wsMessage{Type: "dispatch-status", Data: dispatchStatusData{Message: "Spawned " + newEvents[i].AgentName}})
			case "AgentStartFailed":
				hub.Broadcast(wsMessage{Type: "dispatch-status", Data: dispatchStatusData{Message: "Spawn failed: " + newEvents[i].AgentName}})
			}
		}
		// Any hook activity may have changed confirm state; push the
		// current pending set so dialogs appear without polling.
		if s.Confirms != nil {
			hub.Broadcast(wsMessage{Type: "confirms", Data: s.Confirms.Snapshot()})
		}
	}
}

var upgrader = websocket.Upgrader{
	// Origin is already enforced for every route by guardOrigin; the
	// upgrader must not apply its own same-host default on top, which
	// would reject Bearer-authenticated non-browser clients.
	CheckOrigin: func(*http.Request) bool { return true },
}

// handleWS upgrades the connection and streams push messages until the
// client goes away.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		s.writeError(w, http.StatusServiceUnavailable, "push not available")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	defer func() { _ = conn.Close() }()

	ch := s.hub.attach()
	defer s.hub.detach(ch)

	// Reader goroutine: drains client frames (pong handling) and signals
	// disconnect. The GUI protocol has no client→server messages.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, readErr := conn.ReadMessage(); readErr != nil {
				return
			}
		}
	}()

	pings := time.NewTicker(wsPingInterval)
	defer pings.Stop()

	for {
		select {
		case <-done:
			return
		case msg := <-ch:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		case <-pings.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
