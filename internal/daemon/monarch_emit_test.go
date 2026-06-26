package daemon

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/monarch"
)

// fakeSink captures emitted events for assertions.
type fakeSink struct {
	mu     sync.Mutex
	events []monarch.Event
}

func (f *fakeSink) Send(e monarch.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeSink) all() []monarch.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]monarch.Event(nil), f.events...)
}

func TestMonarchTypeForHook(t *testing.T) {
	cases := []struct {
		name      string
		event     string
		notify    string
		wantType  monarch.EventType
		wantState monarch.State
		wantOK    bool
	}{
		{"start", "SessionStart", "", monarch.EventAgentStart, monarch.StatePlanning, true},
		{"end", "SessionEnd", "", monarch.EventAgentStop, monarch.StateStopped, true},
		{"pre", "PreToolUse", "", monarch.EventAgentStart, monarch.StateCoding, true},
		{"post", "PostToolUse", "", monarch.EventAgentStart, monarch.StateCoding, true},
		{"perm", "PermissionRequest", "", monarch.EventAgentStart, monarch.StateBlocked, true},
		{"notify-perm", "Notification", "permission_prompt", monarch.EventAgentStart, monarch.StateBlocked, true},
		{"notify-other", "Notification", "info", "", "", false},
		{"unknown", "Stop", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			et, state, ok := monarchTypeForHook(tc.event, tc.notify)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantType, et)
			assert.Equal(t, tc.wantState, state)
		})
	}
}

func TestHashAgentID(t *testing.T) {
	id := hashAgentID("human-agent-x", "")
	assert.Regexp(t, `^agent-[0-9a-f]{8}$`, id)
	assert.Equal(t, id, hashAgentID("human-agent-x", ""), "stable for same input")
	assert.NotContains(t, id, "human-agent-x", "raw name never leaks into the id")

	assert.Equal(t, "", hashAgentID("", ""), "empty input yields empty id")
	assert.NotEqual(t, id, hashAgentID("human-agent-y", ""), "different names differ")
}

func TestParseTicketKey(t *testing.T) {
	assert.Equal(t, "HUM-59", parseTicketKey("Implement HUM-59: add validation"))
	assert.Equal(t, "SC-110", parseTicketKey("[SC-110] do the thing"))
	assert.Equal(t, "", parseTicketKey("no key here"))
}

func TestEmitMonarch_nilSinkNoop(t *testing.T) {
	s := &Server{} // MonarchSink nil, DaemonID empty
	assert.NotPanics(t, func() {
		s.emitMonarch(hookevents.Event{EventName: "SessionStart", SessionID: "s1"})
		s.emitMonarchStop("agent-1")
	})
}

func TestEmitMonarch_emitsStart(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{MonarchSink: sink, DaemonID: "daemon-abcd1234"}

	s.emitMonarch(hookevents.Event{EventName: "SessionStart", SessionID: "s1", Cwd: "/home/dev/cli"})

	events := sink.all()
	require.Len(t, events, 1, "SessionStart emits one event (planning, no token event)")
	assert.Equal(t, monarch.EventAgentStart, events[0].Type)
	assert.Equal(t, monarch.StatePlanning, events[0].State)
	assert.Equal(t, "daemon-abcd1234", events[0].DaemonID)
	assert.Equal(t, "cli", events[0].Repo)
	assert.NotEmpty(t, events[0].AgentID)
}

func TestEmitMonarch_codingEmitsOnlyStart(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{MonarchSink: sink, DaemonID: "daemon-1"}

	s.emitMonarch(hookevents.Event{EventName: "PreToolUse", SessionID: "s1", Cwd: "/x/cli"})

	events := sink.all()
	require.Len(t, events, 1, "coding with no token counts emits only agent.start")
	assert.Equal(t, monarch.EventAgentStart, events[0].Type)
	assert.Equal(t, monarch.StateCoding, events[0].State)
}

func TestEmitMonarch_emitsRealTokensFromTranscript(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{MonarchSink: sink, DaemonID: "daemon-1"}

	// SessionEnd carrying transcript-parsed counts maps to agent.stop and also
	// emits a populated tokens.used.
	s.emitMonarch(hookevents.Event{
		EventName: "SessionEnd", SessionID: "s1", Cwd: "/x/cli",
		InputTokens: 1200, OutputTokens: 300, CacheRead: 5000,
	})

	events := sink.all()
	require.Len(t, events, 2, "tokens.used (burn) plus the agent.stop lifecycle event")
	assert.Equal(t, monarch.EventTokensUsed, events[0].Type)
	require.NotNil(t, events[0].Payload)
	assert.Equal(t, 1200, events[0].Payload.InputTokens)
	assert.Equal(t, 300, events[0].Payload.OutputTokens)
	assert.Equal(t, 5000, events[0].Payload.CacheRead)
	assert.Equal(t, monarch.EventAgentStop, events[1].Type)
}

func TestEmitMonarch_enrichesFromAgentMeta(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{
		MonarchSink: sink,
		DaemonID:    "daemon-1",
		AgentMetaReader: func(name string) (cwd, prompt string, ok bool) {
			assert.Equal(t, "human-agent-42", name)
			return "/repos/human/cli", "Implement HUM-143: monarch console", true
		},
	}

	s.emitMonarch(hookevents.Event{EventName: "SessionStart", SessionID: "s1", AgentName: "human-agent-42", Cwd: "/fallback"})

	events := sink.all()
	require.Len(t, events, 1)
	assert.Equal(t, "cli", events[0].Repo, "meta cwd preferred over hook cwd")
	assert.Equal(t, "HUM-143", events[0].TicketKey)
}

func TestEmitMonarchStop_emitsStop(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{MonarchSink: sink, DaemonID: "daemon-1"}

	s.emitMonarchStop("human-agent-7")

	events := sink.all()
	require.Len(t, events, 1)
	assert.Equal(t, monarch.EventAgentStop, events[0].Type)
	assert.Equal(t, monarch.StateStopped, events[0].State)
}
