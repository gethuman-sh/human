//go:build wailsapp

package main

import (
	"context"
	_ "embed"
	"time"

	"fyne.io/systray"

	"github.com/gethuman-sh/human/internal/claude/monitor"
)

// A lowercase "h" in white, 44x44, drawn from rectangles rather than rendered
// from a font — at panel size the two are indistinguishable and this needs no
// font dependency. The application icon is deliberately NOT reused: a detailed
// 1024x1024 image scaled into a 22px panel slot reads as a smudge, and a panel
// glyph has to survive that scale.
//
//go:embed build/trayicon.png
var trayIcon []byte

// trayRefreshInterval is how often the menu re-reads what is running.
//
// Ages tick in the menu, so it must refresh even when nothing starts or stops,
// and reading agent metadata is a local file scan — cheap enough that a short
// interval buys accuracy for nothing. It is a safety net as much as a clock:
// the subscribe stream already pushes starts and stops, and this covers a
// stream that dropped without anyone noticing.
const trayRefreshInterval = 5 * time.Second

// runTray puts an icon in the system tray whose menu lists what the machine is
// working on right now.
//
// The menu is a readout, with no action in it. It used to offer "Open board",
// and clicking an entry raised the window — neither works under Wayland, where
// a client cannot raise itself: focus is granted through an activation token
// obtained from a user interaction, and a tray click arrives over D-Bus as a
// dbusmenu event, a protocol with no token to pass. The compositor is entitled
// to ignore the request and does. A menu item that silently does nothing is
// worse than no item, so there are none.
//
// It answers the question the board answers, without the board being open or in
// focus — a glance at the panel instead of a window switch. Read-only on
// purpose for now: clicking an entry raises the board rather than acting on the
// run, so nothing here can start, stop or disturb work.
//
// Best-effort by construction. A desktop with no system tray, or a panel that
// refuses the icon, must cost the board nothing: systray.Run simply never
// reports ready and the app carries on.
func (a *App) runTray(ctx context.Context) {
	systray.Run(func() { a.onTrayReady(ctx) }, func() {})
}

func (a *App) onTrayReady(ctx context.Context) {
	systray.SetIcon(trayIcon)
	systray.SetTitle("")
	systray.SetTooltip(monitor.TrayTitle(nil))

	// A permanent first line, so the menu always has something to open with and
	// always says something. Without it an idle machine hides every entry, the
	// menu is empty, and the icon reads as broken at precisely the moment the
	// honest answer is "nothing is running".
	status := systray.AddMenuItem("", "")
	status.Disable()

	// The entries are rebuilt into a fixed set of slots rather than recreated:
	// systray has no way to remove an item, so a menu that grew an item per
	// refresh would accumulate every agent that ever ran.
	slots := make([]*systray.MenuItem, trayMenuSlots)
	for i := range slots {
		slots[i] = systray.AddMenuItem("", "")
		slots[i].Disable()
		slots[i].Hide()
	}

	a.refreshTray(ctx, status, slots)

	// onReady MUST return. systray holds a WaitGroup across it and every menu
	// request from the panel begins by waiting on that group — so a refresh loop
	// running here would leave the menu unanswerable forever, and the icon would
	// sit in the panel doing nothing when clicked.
	go a.keepTrayCurrent(ctx, status, slots)
}

// keepTrayCurrent re-reads what is running until the app exits.
func (a *App) keepTrayCurrent(ctx context.Context, status *systray.MenuItem, slots []*systray.MenuItem) {
	ticker := time.NewTicker(trayRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshTray(ctx, status, slots)
		}
	}
}

// trayMenuSlots bounds the menu. A machine running more agents than this has a
// board to look at; a panel menu that long is not a glance any more.
const trayMenuSlots = 12

// refreshTray writes the current runs into the fixed slots, hiding the rest.
func (a *App) refreshTray(ctx context.Context, status *systray.MenuItem, slots []*systray.MenuItem) {
	// The same discovery the board's running-agents panel uses, so the tray and
	// the board can never give different answers to one question.
	finder, dc := buildInstanceFinder()
	snap := monitor.New(finder, dc).FetchFull(ctx)
	if snap.Err != nil {
		// Failing to look is not the same as finding nothing, and the tray must
		// not report the second when it means the first.
		const unknown = "human — cannot read what is running"
		status.SetTitle(unknown)
		systray.SetTooltip(unknown)
		return
	}
	entries := monitor.TrayEntries(snap.Instances, time.Now())
	status.SetTitle(monitor.TrayTitle(entries))
	systray.SetTooltip(monitor.TrayTitle(entries))
	for i, slot := range slots {
		if i >= len(entries) {
			slot.Hide()
			continue
		}
		slot.SetTitle(entries[i].Label)
		slot.Show()
	}
}
