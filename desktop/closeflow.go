//go:build wailsapp

package main

import (
	"context"
	"os/exec"
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// closeBusyPollInterval is how often "Wait and close" re-checks whether the
// daemon has gone idle.
const closeBusyPollInterval = 3 * time.Second

// beforeClose is the Wails OnBeforeClose hook (wired in main.go). It NEVER
// blocks: on Windows, Wails calls this synchronously on the native window
// message-loop thread (winc's OnClose → Frontend.Quit → OnBeforeClose), so
// waiting here for anything would freeze the whole app — the SC-2865 class of
// bug this ticket's last acceptance criterion names. Every real decision
// (busy check, dialog, waiting) happens on the goroutine spawned below; this
// function only flips fast, lock-free flags and returns.
//
// Wails' own runtime.Quit re-invokes this hook (confirmed in wails v2's
// platform frontends) — that is how a delayed "actually close now" is
// expressed: stopAndQuit sets readyToQuit first, then calls runtime.Quit,
// and THIS SECOND call returns false (permit close) instead of true.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.readyToQuit.Load() {
		return false
	}
	if a.closeInFlight.Swap(true) {
		// A close decision (dialog shown, or a "wait" already polling) is
		// already underway; a second click on the window's close button must
		// not stack a second dialog or a second wait loop.
		return true
	}
	go a.runCloseFlow(ctx)
	return true
}

// runCloseFlow makes the real decision off the native thread: idle stops the
// daemon and quits with no prompt; busy hands off to the frontend's
// three-way dialog via an event, exactly like main.go's subscribe→EventsEmit
// bridge already does for board changes.
func (a *App) runCloseFlow(ctx context.Context) {
	busy, err := a.DaemonBusy()
	if err != nil || !busy {
		a.stopAndQuit(ctx)
		return
	}
	wailsruntime.EventsEmit(ctx, "app:close-busy")
}

// ResolveClose is the frontend's answer to the "app:close-busy" three-way
// dialog (Cancel / Stop anyway / Wait and close). Bound so the dialog's
// choice crosses back into Go without OnBeforeClose ever blocking for it.
func (a *App) ResolveClose(choice string) error {
	switch choice {
	case "stop":
		a.stopAndQuit(a.ctx)
	case "wait":
		go a.waitThenClose(a.ctx)
	default: // "cancel", or any unrecognised value — never close on a bad one
		a.closeInFlight.Store(false)
	}
	return nil
}

// waitThenClose polls until the daemon is no longer busy (or becomes
// unreachable) and only then stops it and quits — "Wait and close" never
// forcibly ends in-flight work. Bounded by ctx so an eventual app shutdown
// stops the poll rather than leaking it.
func (a *App) waitThenClose(ctx context.Context) {
	ticker := time.NewTicker(closeBusyPollInterval)
	defer ticker.Stop()
	for {
		busy, err := a.DaemonBusy()
		if err != nil || !busy {
			a.stopAndQuit(ctx)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// stopAndQuit stops the daemon this app manages — the identical,
// provenance-agnostic StopIfRunning SwitchProject already uses, so an
// adopted daemon is stopped the same as one this app launched (AC4) — clears
// the session marker so a cleanly stopped daemon is never later reported
// orphaned, and asks Wails to really close: readyToQuit makes the re-entrant
// OnBeforeClose call return false this time.
func (a *App) stopAndQuit(ctx context.Context) {
	if cliPath, err := daemon.ResolveCLIPath(exec.LookPath); err == nil {
		_ = daemon.StopIfRunning(daemon.DefaultRunner, cliPath)
	}
	_ = a.session.Clear()
	a.readyToQuit.Store(true)
	wailsruntime.Quit(ctx)
}
