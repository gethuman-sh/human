package cmdtui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/monitor"
	"github.com/gethuman-sh/human/internal/daemon"
)

// baseTime is a fixed reference time so relative timestamps in the
// panel output are deterministic across runs.
var baseTime = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

func makeEvents(hosts ...string) []daemon.NetworkEvent {
	out := make([]daemon.NetworkEvent, 0, len(hosts))
	for i, h := range hosts {
		out = append(out, daemon.NetworkEvent{
			Source:   "proxy",
			Status:   "forward",
			Host:     h,
			Count:    1,
			LastSeen: baseTime.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func TestRenderDomainsPanel_zeroEvents(t *testing.T) {
	out := renderDomainsPanel(nil, 120, 20, baseTime.Add(5*time.Second))
	assert.Equal(t, "", out)

	out = renderDomainsPanel([]daemon.NetworkEvent{}, 120, 20, baseTime.Add(5*time.Second))
	assert.Equal(t, "", out)
}

func TestRenderDomainsPanel_extremelyShortTerminal(t *testing.T) {
	events := makeEvents("a.example.com", "b.example.com", "c.example.com")
	out := renderDomainsPanel(events, 120, 1, baseTime.Add(5*time.Second))
	assert.Equal(t, "", out, "available=1 should yield its space to the footer")

	out = renderDomainsPanel(events, 120, 0, baseTime.Add(5*time.Second))
	assert.Equal(t, "", out, "available=0 should collapse")

	out = renderDomainsPanel(events, 120, -3, baseTime.Add(5*time.Second))
	assert.Equal(t, "", out, "negative available should collapse")
}

func TestRenderDomainsPanel_shortTerminal(t *testing.T) {
	events := makeEvents("a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com")
	// available=3 -> 1 header row + 2 data rows.
	out := renderDomainsPanel(events, 120, 3, baseTime.Add(5*time.Second))
	require.NotEmpty(t, out)

	lines := strings.Split(out, "\n")
	assert.Len(t, lines, 3, "1 header + 2 rows")
	assert.Contains(t, lines[0], "Network")
	// Newest-on-top means the last event (e.example.com) should be the
	// first data row.
	assert.Contains(t, lines[1], "e.example.com")
	assert.Contains(t, lines[2], "d.example.com")
}

func TestRenderDomainsPanel_tallTerminal(t *testing.T) {
	events := makeEvents("a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com")
	out := renderDomainsPanel(events, 120, 20, baseTime.Add(5*time.Second))

	lines := strings.Split(out, "\n")
	// 1 header + 5 rows = 6 lines.
	assert.Len(t, lines, 6)
	assert.Contains(t, lines[0], "Network")
}

func TestRenderDomainsPanel_newestOnTop(t *testing.T) {
	// Insertion order [A, B, C] — C is newest.
	events := makeEvents("A.example.com", "B.example.com", "C.example.com")
	out := renderDomainsPanel(events, 120, 20, baseTime.Add(5*time.Second))

	lines := strings.Split(out, "\n")
	assert.Contains(t, lines[1], "C.example.com", "first data row should be newest")
	assert.Contains(t, lines[2], "B.example.com")
	assert.Contains(t, lines[3], "A.example.com", "last data row should be oldest")
}

func TestRenderDomainsPanel_oldestPushedOutWhenFull(t *testing.T) {
	events := makeEvents(
		"01.example.com", "02.example.com", "03.example.com", "04.example.com", "05.example.com",
		"06.example.com", "07.example.com", "08.example.com", "09.example.com", "10.example.com",
	)
	// available=6 -> 1 header + 5 data rows. The oldest 5 rows should
	// be dropped so only the newest 5 (06..10) appear, with 10 on top.
	out := renderDomainsPanel(events, 120, 6, baseTime.Add(15*time.Second))

	lines := strings.Split(out, "\n")
	assert.Len(t, lines, 6)
	assert.Contains(t, lines[1], "10.example.com")
	assert.Contains(t, lines[5], "06.example.com")

	// Dropped rows should not appear at all.
	for _, dropped := range []string{"01.example.com", "02.example.com", "03.example.com", "04.example.com", "05.example.com"} {
		assert.NotContains(t, out, dropped)
	}
}

func TestRenderDomainsPanel_burstDedupRender(t *testing.T) {
	events := []daemon.NetworkEvent{
		{Source: "proxy", Status: "forward", Host: "github.com", Count: 47, LastSeen: baseTime},
	}
	out := renderDomainsPanel(events, 120, 10, baseTime.Add(2*time.Second))
	assert.Contains(t, out, "github.com x47", "burst dedup should surface the count suffix")
}

func TestRenderDomainsPanel_failSourceColouring(t *testing.T) {
	events := []daemon.NetworkEvent{
		{Source: "fail", Status: "dial-fail", Host: "broken.example.com", Count: 1, LastSeen: baseTime},
	}
	out := renderDomainsPanel(events, 120, 10, baseTime.Add(3*time.Second))
	// errorStyle renders ANSI escape codes around "[fail]". We can't
	// easily compare exact codes because lipgloss may disable them in
	// test environments without a TTY, but we can at least verify the
	// tag text is present.
	assert.Contains(t, out, "[fail]")
	assert.Contains(t, out, "broken.example.com")
}

func TestRenderDomainsPanel_emptyHostRendersPlaceholder(t *testing.T) {
	events := []daemon.NetworkEvent{
		{Source: "fail", Status: "no-sni", Host: "", Count: 1, LastSeen: baseTime},
	}
	out := renderDomainsPanel(events, 120, 10, baseTime.Add(1*time.Second))
	assert.Contains(t, out, "(no sni)")
}

func TestRenderDomainsPanel_longHostTruncated(t *testing.T) {
	longHost := strings.Repeat("a", 500) + ".example.com"
	events := []daemon.NetworkEvent{
		{Source: "proxy", Status: "forward", Host: longHost, Count: 1, LastSeen: baseTime},
	}
	out := renderDomainsPanel(events, 80, 10, baseTime.Add(1*time.Second))
	// Overall output width per row should stay bounded — we only check
	// that the output does not contain the full 500-byte host.
	assert.NotContains(t, out, strings.Repeat("a", 500))
}

// TestRenderDomainsPanel_noOverlapWithFooter is the HUM-59 end-to-end
// guard on the bottom-anchored layout math in View() (tui.go:913-916).
// The domains panel must yield space to the footer when the terminal
// is too short, so a full View() render for an m.height=10 model with
// many queued network events must emit at most m.height newlines.
func TestRenderDomainsPanel_noOverlapWithFooter(t *testing.T) {
	const terminalHeight = 10

	// Populate far more events than can fit so the panel has to truncate
	// itself rather than push the footer off-screen.
	hosts := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		hosts = append(hosts, "host-"+strings.Repeat("x", i%5)+".example.com")
	}

	m := testModel()
	m.width = 120
	m.height = terminalHeight
	m.snap = testSnapshot(func(s *monitor.Snapshot) {
		s.NetworkEvents = makeEvents(hosts...)
	})

	view := m.View()
	lineCount := strings.Count(view, "\n")
	assert.LessOrEqual(t, lineCount, terminalHeight,
		"View() must not emit more newlines than m.height — footer would be clipped")
}

func TestDomainSourceStyle(t *testing.T) {
	assert.Equal(t, errorStyle, domainSourceStyle("fail", "dial-fail"))
	assert.Equal(t, errorStyle, domainSourceStyle("proxy", "block"))
	assert.Equal(t, warningStyle, domainSourceStyle("proxy", "intercept"))
	assert.Equal(t, specialStyle, domainSourceStyle("proxy", "forward"))
	assert.Equal(t, specialStyle, domainSourceStyle("oauth", "callback"))
	assert.Equal(t, subtleStyle, domainSourceStyle("unknown", "whatever"))
}
