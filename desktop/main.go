//go:build wailsapp

package main

import (
	"context"
	"embed"
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	// Before wails.Run installs its own signal handler: a terminal Ctrl-C must
	// end the app outright rather than being answered by the window's close
	// dialog (SC-3292). See signalexit.go.
	app.installConsoleExit()

	// REGRESSION GUARD: this app must be built with `wails build` (make desktop)
	// — NOT `go build ./desktop/`. Wails' main() requires the build tags that
	// only `wails build`/`wails dev` inject; a plain `go build` links but the app
	// fails at startup. A green `go build`/`go vet ./desktop/...` is therefore
	// NOT evidence the app runs. See docs/desktop-app.md.
	err := wails.Run(&options.App{
		Title:  "human — workflow board",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
			// Serves /mockups/<slug>/<file> from project directories on
			// disk so the Mockups view can iframe /human-mockups output
			// without embedding it in the binary.
			Middleware: mockupMiddleware,
		},
		OnStartup: app.startup,
		// See closeflow.go: never blocks, decides idle/busy on a background
		// goroutine (SC-3015 / SC-2865 guard).
		OnBeforeClose: app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		panic(err)
	}
}

// startup stores the lifecycle context and starts the daemon subscription
// bridge: subscribe once, and on every daemon change
// event emit a "board:changed" event to the frontend, which re-calls Cards().
// There is intentionally no Go-side polling loop; the frontend keeps a slow
// safety-net re-fetch (board.ts BOARD_SAFETY_POLL_MS) solely so tracker edits
// made outside human — which produce no daemon event — still surface.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.subscribe(ctx)
	// The tray is a second, smaller window onto the same question the board
	// answers: what is running. It must never be able to hold the board up, so
	// it runs on its own goroutine and its failures stay its own.
	go a.runTray(ctx)
}

// subscribe opens the daemon subscribe stream and forwards each change as a
// frontend event. daemon.Subscribe returns (channel, cancel, error); on any
// failure (e.g. daemon not yet up) it retries after a short backoff so the
// board recovers without a restart.
func (a *App) subscribe(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		info, err := daemon.ReadInfo()
		if err != nil {
			a.backoff(ctx)
			continue
		}
		events, cancel, err := daemon.Subscribe(info.Addr, info.Token)
		if err != nil {
			a.backoff(ctx)
			continue
		}
		a.drain(ctx, events)
		cancel()
		// The stream dropped (daemon restarted or connection lost); loop to
		// re-subscribe after a brief pause.
		a.backoff(ctx)
	}
}

// drain forwards subscribe events to the frontend until the stream closes or the
// app shuts down.
func (a *App) drain(ctx context.Context, events <-chan daemon.SubscribeEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			wailsruntime.EventsEmit(a.ctx, "board:changed")
		}
	}
}

// backoff pauses before a re-subscribe attempt, aborting early if the app is
// shutting down.
func (a *App) backoff(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
}

// dockerAvailable reports whether a Docker engine is reachable ON THIS MACHINE.
// The frontend uses the flag to disable the agent-launching drop targets
// (planning, impl, verification) with a tooltip while leaving the Done drop zone
// enabled, since Done only pushes a branch and opens a PR.
//
// This is the FALLBACK answer only. The flag describes the host that launches
// agents, which is the daemon's — so the composed board carries the daemon's own
// doctor verdict, and this local probe is used just when composing locally
// against a daemon too old to serve one (SC-2132).
func dockerAvailable() bool {
	dc, err := devcontainer.NewDockerClient()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A cheap round-trip to the engine: listing images fails fast when the
	// daemon socket is absent or the engine is stopped.
	if _, err := dc.ImageList(ctx, devcontainer.ImageListOptions{}); err != nil {
		return false
	}
	return true
}
