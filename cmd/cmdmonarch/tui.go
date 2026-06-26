package cmdmonarch

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/monarch"
)

const (
	defaultWidth   = 80
	refreshEvery   = 2 * time.Second
	workBoardSince = 24 * time.Hour
	// presenceWindow is the live-capacity window: a daemon counts as connected
	// only if monarch saw an event (a heartbeat or real work) within it. It is a
	// few heartbeat intervals so a single dropped heartbeat does not drop the
	// daemon, while a daemon that actually stops disappears within seconds.
	presenceWindow = 3 * monarch.DefaultHeartbeatInterval
	storeReadLimit = 3 * time.Second
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	titleStyle  = lipgloss.NewStyle().Bold(true).MarginTop(1)
	dimStyle    = lipgloss.NewStyle().Faint(true)
)

// model is the monarch console state. It reads its own SQLite store on a ticker;
// there is no dependency on a running daemon.
type model struct {
	store    *monarch.Store
	width    int
	height   int
	quitting bool

	board []monarch.WorkItem
	burnT []monarch.BurnRow
	burnR []monarch.BurnRow
	cap   monarch.Capacity
}

func newModel(store *monarch.Store) model {
	return model{store: store, width: defaultWidth}
}

type tickMsg time.Time

type dataMsg struct {
	board []monarch.WorkItem
	burnT []monarch.BurnRow
	burnR []monarch.BurnRow
	cap   monarch.Capacity
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchCmd(m.store), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// fetchCmd runs the four store reads and returns a dataMsg. Read errors collapse
// to empty slices so a transient store hiccup never tears down the console.
func fetchCmd(store *monarch.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), storeReadLimit)
		defer cancel()
		now := time.Now().UTC()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		board, _ := store.WorkBoard(ctx, now.Add(-workBoardSince))
		burnT, _ := store.BurnByTicket(ctx, dayStart)
		burnR, _ := store.BurnByRepo(ctx, dayStart)
		cap, _ := store.Capacity(ctx, now.Add(-presenceWindow))
		return dataMsg{board: board, burnT: burnT, burnR: burnR, cap: cap}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(fetchCmd(m.store), tickCmd())
	case dataMsg:
		m.board = msg.board
		m.burnT = msg.burnT
		m.burnR = msg.burnR
		m.cap = msg.cap
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("monarch — swarm console"))
	b.WriteString("\n")
	b.WriteString(m.capacityLine())
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Work board"))
	b.WriteString("\n")
	b.WriteString(m.renderBoard())
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Burn (today)"))
	b.WriteString("\n")
	b.WriteString(m.renderBurn())
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("q quit · refreshes every 2s"))
	return b.String()
}

func (m model) capacityLine() string {
	c := m.cap
	return fmt.Sprintf("Capacity: %d daemons · %d busy · %d blocked · %d idle",
		c.Daemons, c.Busy, c.Blocked, c.Idle)
}

// boardColWidths fixes the work-board column layout. Manual lipgloss composition
// keeps the dependency surface identical to cmd/cmdtui (no lipgloss/table).
var boardColWidths = []int{12, 12, 16, 10, 9}

func (m model) renderBoard() string {
	if len(m.board) == 0 {
		return dimStyle.Render("  (no agents in flight)")
	}
	var b strings.Builder
	b.WriteString(boardRow([]string{"DAEMON", "TICKET", "REPO", "STATE", "UPDATED"}, true))
	for _, w := range m.board {
		ticket := w.TicketKey
		if ticket == "" {
			ticket = "—"
		}
		updated := agent.FormatDuration(time.Since(w.UpdatedAt))
		b.WriteString(boardRow([]string{w.DaemonID, ticket, w.Repo, w.State, updated}, false))
	}
	return b.String()
}

func boardRow(cols []string, header bool) string {
	var parts []string
	for i, c := range cols {
		w := boardColWidths[i]
		parts = append(parts, lipgloss.NewStyle().Width(w).Render(truncate(c, w)))
	}
	line := "  " + strings.Join(parts, " ")
	if header {
		return headerStyle.Render(line) + "\n"
	}
	return line + "\n"
}

func (m model) renderBurn() string {
	if len(m.burnT) == 0 && len(m.burnR) == 0 {
		return dimStyle.Render("  (no burn recorded today)")
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render("  By ticket"))
	b.WriteString("\n")
	b.WriteString(renderBurnRows(m.burnT))
	b.WriteString(dimStyle.Render("  By repo"))
	b.WriteString("\n")
	b.WriteString(renderBurnRows(m.burnR))
	return b.String()
}

func renderBurnRows(rows []monarch.BurnRow) string {
	if len(rows) == 0 {
		return dimStyle.Render("    —") + "\n"
	}
	var b strings.Builder
	for _, r := range rows {
		key := r.Key
		if key == "" {
			key = "—"
		}
		total := r.InputTokens + r.OutputTokens + r.CacheCreate + r.CacheRead
		tokens := "—"
		if total > 0 {
			tokens = claude.FormatTokens(total)
		}
		left := lipgloss.NewStyle().Width(20).Render(truncate(key, 20))
		_, _ = fmt.Fprintf(&b, "    %s %s\n", left, tokens)
	}
	return b.String()
}

// truncate shortens s to at most w runes so a long repo/ticket never overflows
// its fixed column and breaks the manual layout.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}
