package monitor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
)

func view(label, slug string, startedMinsAgo int) InstanceView {
	v := InstanceView{Usage: claude.InstanceUsage{Instance: claude.Instance{Label: label}}}
	if startedMinsAgo >= 0 {
		v.Session = &logparser.SessionState{
			Slug:      slug,
			StartedAt: time.Now().Add(-time.Duration(startedMinsAgo) * time.Minute),
		}
	}
	return v
}

// The tray and the board must never disagree about what the machine is doing,
// so the tray reads the same discovery the board's panel reads.
func TestTrayEntries_readsWhatTheBoardShows(t *testing.T) {
	views := []InstanceView{view("Host: human/cli (PID 7241)", "scalable-sleeping-hopper", 5)}

	entries := TrayEntries(views, time.Now())

	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Label, "Host: human/cli (PID 7241)")
	assert.Contains(t, entries[0].Label, "scalable-sleeping-hopper")
	assert.Contains(t, entries[0].Label, "5m")
}

// Newest first: the useful glance is what was just picked up.
func TestTrayEntries_newestFirst(t *testing.T) {
	views := []InstanceView{view("old", "a", 600), view("new", "b", 2)}

	entries := TrayEntries(views, time.Now())

	require.Len(t, entries, 2)
	assert.Contains(t, entries[0].Label, "new")
	assert.Contains(t, entries[1].Label, "old")
}

// An instance whose transcript has not been paired is still running. Saying
// nothing about it would be a detail of discovery presented as an answer.
func TestTrayEntries_instanceWithoutSessionIsStillShown(t *testing.T) {
	entries := TrayEntries([]InstanceView{view("Container: dev (abc123)", "", -1)}, time.Now())

	require.Len(t, entries, 1)
	assert.Equal(t, "Container: dev (abc123)", entries[0].Label)
}

// A session with no slug still gets its duration — the name is a nicety, the
// fact that it is running is not.
func TestTrayEntries_missingSlugKeepsTheDuration(t *testing.T) {
	entries := TrayEntries([]InstanceView{view("Host: x (PID 1)", "", 3)}, time.Now())

	assert.Contains(t, entries[0].Label, "3m")
	assert.NotContains(t, entries[0].Label, "· ·")
}

// Idle is a real answer and has to read as one.
func TestTrayTitle_statesWhatIsRunning(t *testing.T) {
	assert.Equal(t, "human — nothing running", TrayTitle(nil))
	assert.Equal(t, "human — 1 agent running", TrayTitle([]MenuEntry{{}}))
	assert.Equal(t, "human — 3 agents running", TrayTitle([]MenuEntry{{}, {}, {}}))
}

// The caller's slice must not be reordered underneath it.
func TestTrayEntries_doesNotReorderTheCallersSlice(t *testing.T) {
	views := []InstanceView{view("old", "a", 600), view("new", "b", 2)}

	TrayEntries(views, time.Now())

	assert.Equal(t, "old", views[0].Usage.Instance.Label)
}
