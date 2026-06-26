package cmdmonarch

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/monarch"
)

func newTestStore(t *testing.T) *monarch.Store {
	t.Helper()
	s, err := monarch.NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestView_emptyState(t *testing.T) {
	m := newModel(newTestStore(t))
	out := m.View()
	assert.Contains(t, out, "monarch — swarm console")
	assert.Contains(t, out, "Capacity: 0 daemons")
	assert.Contains(t, out, "(no agents in flight)")
	assert.Contains(t, out, "(no burn recorded today)")
}

func TestView_quittingIsEmpty(t *testing.T) {
	m := newModel(newTestStore(t))
	m.quitting = true
	assert.Equal(t, "", m.View())
}

func TestUpdate_quitKeys(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		m := newModel(newTestStore(t))
		_, cmd := m.Update(tea.KeyMsg{Type: keyType(key), Runes: keyRunes(key)})
		require.NotNil(t, cmd, "key %q should produce a quit command", key)
	}
}

func TestUpdate_windowSize(t *testing.T) {
	m := newModel(newTestStore(t))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.Equal(t, 120, updated.(model).width)
	assert.Equal(t, 40, updated.(model).height)
}

func TestUpdate_dataMsgPopulatesView(t *testing.T) {
	m := newModel(newTestStore(t))
	now := time.Now().UTC()
	msg := dataMsg{
		board: []monarch.WorkItem{{DaemonID: "daemon-1", TicketKey: "HUM-143", Repo: "cli", State: "coding", UpdatedAt: now}},
		burnT: []monarch.BurnRow{{Key: "HUM-143", InputTokens: 1500}},
		burnR: []monarch.BurnRow{{Key: "cli", InputTokens: 1500}},
		cap:   monarch.Capacity{Daemons: 1, Busy: 1},
	}
	updated, _ := m.Update(msg)
	out := updated.(model).View()
	assert.Contains(t, out, "daemon-1")
	assert.Contains(t, out, "HUM-143")
	assert.Contains(t, out, "cli")
	assert.Contains(t, out, "1 busy")
	assert.Contains(t, out, "1.5K")
}

func TestFetchCmd_returnsDataMsg(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, store.Insert(context.Background(), monarch.Event{
		Type: monarch.EventAgentStart, DaemonID: "d1", AgentID: "a1", Repo: "cli", State: monarch.StateCoding, TS: now,
	}))

	msg := fetchCmd(store)()
	data, ok := msg.(dataMsg)
	require.True(t, ok)
	require.Len(t, data.board, 1)
	assert.Equal(t, "d1", data.board[0].DaemonID)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "ab…", truncate("abcdef", 3))
	assert.Equal(t, "", truncate("abc", 0))
	assert.Equal(t, "…", truncate("abc", 1))
}

func TestBurnZeroRendersDash(t *testing.T) {
	out := renderBurnRows([]monarch.BurnRow{{Key: "HUM-1"}})
	assert.Contains(t, out, "HUM-1")
	assert.Contains(t, out, "—", "zero burn renders as em dash")
}

// keyType/keyRunes map a friendly key name to a tea.KeyMsg shape.
func keyType(k string) tea.KeyType {
	switch k {
	case "ctrl+c":
		return tea.KeyCtrlC
	case "esc":
		return tea.KeyEsc
	default:
		return tea.KeyRunes
	}
}

func keyRunes(k string) []rune {
	if k == "q" {
		return []rune("q")
	}
	return nil
}
