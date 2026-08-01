package monitor

import (
	"fmt"
	"sort"
	"time"

	"github.com/gethuman-sh/human/internal/agent"
)

// MenuEntry is one line of the tray menu.
type MenuEntry struct {
	// Label is what a person reads: which instance, what it is doing, how long
	// it has been at it.
	Label string
}

// TrayEntries describes what is running right now, longest-running last.
//
// It reads the same discovery the board's running-agents panel reads, so the
// tray and the board can never disagree about what the machine is doing —
// two answers to that question is worse than one answer in one place.
//
// An instance with no matched session is still shown. It is running; the fact
// that its transcript has not been paired yet is a detail of discovery, not a
// reason to tell someone nothing is happening.
func TrayEntries(views []InstanceView, now time.Time) []MenuEntry {
	sorted := make([]InstanceView, len(views))
	copy(sorted, views)
	sort.SliceStable(sorted, func(i, j int) bool {
		return startedAt(sorted[i]).After(startedAt(sorted[j]))
	})

	entries := make([]MenuEntry, 0, len(sorted))
	for _, v := range sorted {
		entries = append(entries, MenuEntry{Label: entryLabel(v, now)})
	}
	return entries
}

// entryLabel renders one instance as
// "Host: human/cli (PID 7241) · 31h51m · scalable-sleeping-hopper".
func entryLabel(v InstanceView, now time.Time) string {
	label := v.Usage.Instance.Label
	started := startedAt(v)
	if started.IsZero() {
		return label
	}
	line := fmt.Sprintf("%s · %s", label, agent.FormatDuration(now.Sub(started)))
	if v.Session != nil && v.Session.Slug != "" {
		line += " · " + v.Session.Slug
	}
	return line
}

// startedAt is when this instance's work began, or the zero time when no
// session has been matched to it.
func startedAt(v InstanceView) time.Time {
	if v.Session == nil {
		return time.Time{}
	}
	return v.Session.StartedAt
}

// TrayTitle summarises the menu, so an idle machine says so plainly rather
// than presenting an empty menu that reads as a broken icon.
func TrayTitle(entries []MenuEntry) string {
	switch len(entries) {
	case 0:
		return "human — nothing running"
	case 1:
		return "human — 1 agent running"
	default:
		return fmt.Sprintf("human — %d agents running", len(entries))
	}
}
