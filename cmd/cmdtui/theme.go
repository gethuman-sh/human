package cmdtui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Human brand colors.
const (
	humanGold   = lipgloss.Color("#fac86a") // warm gold — titles, highlights
	humanYellow = lipgloss.Color("#ffee97") // light yellow — instance labels
	humanPink   = lipgloss.Color("#d73d73") // pink — errors
	humanTeal   = lipgloss.Color("#4ee8c4") // teal — success, ready, running
	humanPurple = lipgloss.Color("#555598") // muted purple — subtle, secondary
	humanRed    = lipgloss.Color("#e05050") // red — busy, working, accent
	humanBlue   = lipgloss.Color("#6ab0f3") // blue — waiting for user input
	humanOrange = lipgloss.Color("#f0a050") // orange — confirmation pending
)

// Model progress-bar colors, keyed by family prefix of the display name
// ("opus 4.8" → "opus") so new versions inherit their family's color
// without a map update.
var modelFamilyColors = []struct{ prefix, color string }{
	{"opus", "#fac86a"},   // gold
	{"sonnet", "#4ee8c4"}, // teal
	{"haiku", "#555598"},  // purple
	{"fable", "#c86afa"},  // violet
}

// Styles used across the TUI.
var (
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(humanGold)
	subtleStyle        = lipgloss.NewStyle().Foreground(humanPurple)
	busyInstanceStyle  = lipgloss.NewStyle().Bold(true).Foreground(humanPink)
	idleInstanceStyle  = lipgloss.NewStyle().Bold(true).Foreground(humanTeal)
	slugStyle          = lipgloss.NewStyle().Foreground(humanPurple).Italic(true)
	accentStyle        = lipgloss.NewStyle().Foreground(humanRed)
	specialStyle       = lipgloss.NewStyle().Foreground(humanTeal)
	warningStyle       = lipgloss.NewStyle().Foreground(humanYellow)
	waitingStyle       = lipgloss.NewStyle().Foreground(humanBlue)
	errorStyle         = lipgloss.NewStyle().Foreground(humanPink)
	ruleStyle          = lipgloss.NewStyle().Foreground(humanPurple)
	selectedStyle      = lipgloss.NewStyle().Bold(true).Foreground(humanGold)
	confirmStyle       = lipgloss.NewStyle().Bold(true).Foreground(humanOrange)
	activeTabStyle     = lipgloss.NewStyle().Bold(true).Foreground(humanGold).Background(lipgloss.Color("#333355"))
	inactiveTabStyle   = lipgloss.NewStyle().Foreground(humanPurple)
	dialogStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(humanGold).Padding(1, 2)
	confirmDialogStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(humanOrange).Padding(1, 2)
)

func modelColor(name string) string {
	for _, fc := range modelFamilyColors {
		if strings.HasPrefix(name, fc.prefix) {
			return fc.color
		}
	}
	return "#fac86a" // default gold
}
