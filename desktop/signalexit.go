//go:build wailsapp

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// installConsoleExit makes a terminal signal end the app the way it did before
// the window-close confirmation existed: immediately, and without touching the
// daemon.
//
// It is needed because Wails installs its own SIGINT/SIGTERM handler
// (wails/v2/internal/signal), which replaces Go's default terminate-on-Ctrl-C
// and routes the signal into App.Shutdown → Frontend.Quit → OnBeforeClose. With
// closeflow.go wired there, Ctrl-C stopped ending the app: it asked its
// three-way question in a dialog inside the window while the person who pressed
// it was looking at a terminal, and Wails' handler reads its channel exactly
// once, so no later Ctrl-C could reach it either (SC-3292).
//
// A console launch is also a statement about ownership: whoever starts the app
// from a shell manages their own daemon, so this path deliberately performs no
// busy check and no daemon stop. It only drops the app's session marker — a
// local file, never the daemon — so the next launch treats a daemon left
// running as intentional rather than crash-orphaned, which is exactly what it
// was before SC-3015 introduced the marker. Force-quit still leaves the marker
// behind and is still detected as an orphan.
//
// Go delivers a signal to every registered channel, so Wails' handler still
// runs; it simply loses to the os.Exit below, whichever order they start in.
func (a *App) installConsoleExit() {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		// A second signal must always end the process, even if clearing the
		// marker below is somehow held up (a hung home directory) — never
		// repeat the "Ctrl-C does nothing, twice" failure this fixes.
		go func() {
			<-sigs
			os.Exit(1)
		}()
		_ = a.session.Clear()
		// Zero, not 130: before the close hook existed this path unwound
		// through gtk_main_quit and main returned normally, and a console
		// launch should keep seeing the same exit status.
		os.Exit(0)
	}()
}
