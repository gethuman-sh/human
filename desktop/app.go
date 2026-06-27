//go:build wailsapp

// Package main implements the workflow-board desktop app (Wails v2).
//
// The whole file set is guarded by the `wailsapp` build tag because Wails is a
// cgo backend (webkit2gtk / WebView2 / Obj-C) that cannot compile on a plain
// toolchain without the native webview headers. The tag keeps `go vet ./...`,
// `go list ./...` and the existing CI Linux build green — the desktop binary is
// produced only via `wails build` on the 3-runner matrix (see Makefile +
// .github/workflows/desktop.yml).
//
// The tag is deliberately NOT named `desktop`: Wails reserves `desktop` as its
// own output-mode tag and strips it before the host-side binding-generation
// build, which would hide every file here and break `wails build`. A neutral
// tag survives both the binding pass and the final compile, while Wails still
// adds `desktop` itself for the cgo backend selection.
package main

import (
	"context"

	"github.com/gethuman-sh/human/internal/daemon"
)

// App is the Go backend bound into the webview via options.App.Bind. Every
// method here is callable from the TypeScript frontend. The app talks ONLY to
// the daemon client (daemon.GetTrackerIssues / daemon.BoardTransition /
// daemon.Subscribe) — never directly to a tracker or forge — so all credential
// handling, role resolution and the destructive-confirm bypass stay in the
// daemon, exactly as the TUI does it.
type App struct {
	ctx context.Context
}

// NewApp constructs the backend. Wails injects the lifecycle context via
// startup, so there is nothing to wire here.
func NewApp() *App {
	return &App{}
}

// Card is the flat, frontend-facing shape of one board ticket: a PM issue joined
// with its derived BoardCard. The frontend renders columns purely from these —
// it never re-derives a stage from comments.
type Card struct {
	Key            string `json:"key"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	Stage          string `json:"stage"`
	State          string `json:"state"`
	EngineeringKey string `json:"engineeringKey,omitempty"`
	Branch         string `json:"branch,omitempty"`
	PRURL          string `json:"prURL,omitempty"`
	Error          string `json:"error,omitempty"`
}

// BoardData is the full payload the frontend renders: the flat card list plus an
// optional fetch error (surfaced as a banner) and a dockerAvailable flag the
// frontend uses to disable the agent-launching drop targets.
type BoardData struct {
	Cards           []Card `json:"cards"`
	Error           string `json:"error,omitempty"`
	DockerAvailable bool   `json:"dockerAvailable"`
}

// Cards fetches the current board state from the daemon and flattens the single
// PM-role result into a card list, dropping hidden cards. v1 is single
// project/tracker, so we take the first PM-role result. Any per-result fetch
// error is surfaced to the frontend rather than dropped, so the user sees a
// banner instead of an empty board.
func (a *App) Cards() (BoardData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return BoardData{}, err
	}

	results, err := daemon.GetTrackerIssues(info.Addr, info.Token)
	if err != nil {
		return BoardData{}, err
	}

	data := BoardData{DockerAvailable: dockerAvailable()}
	pm, ok := firstPMResult(results)
	if !ok {
		// No PM-role tracker configured yet: render five empty columns rather
		// than erroring, matching the "zero PM issues" requirement.
		return data, nil
	}
	if pm.Err != "" {
		data.Error = pm.Err
	}

	for _, issue := range pm.Issues {
		card := pm.BoardCards[issue.Key]
		if card.Stage == daemon.BoardHidden {
			// Closed PM ticket that never entered the pipeline — not shown.
			continue
		}
		stage := card.Stage
		if stage == "" {
			// Defensive: a PM issue with no derived card sits in Backlog.
			stage = daemon.BoardBacklog
		}
		data.Cards = append(data.Cards, Card{
			Key:            issue.Key,
			Title:          issue.Title,
			URL:            issue.URL,
			Stage:          string(stage),
			State:          string(card.State),
			EngineeringKey: card.EngineeringKey,
			Branch:         card.Branch,
			PRURL:          card.PRURL,
			Error:          card.Error,
		})
	}
	return data, nil
}

// Transition advances a card one stage by delegating to the daemon's
// board-transition route. The daemon is authoritative: it re-derives the card
// from live comments and enforces forward-only/gated rules, so an out-of-date
// optimistic move in the UI is corrected on the next Cards() reconcile.
func (a *App) Transition(pmKey, pmTitle, from, to string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.BoardTransition(info.Addr, info.Token, daemon.BoardTransitionRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
		From:    daemon.BoardStage(from),
		To:      daemon.BoardStage(to),
	})
}

// firstPMResult returns the first PM-role tracker result. v1 supports a single
// PM project; selecting by role (not key prefix) mirrors the daemon's own
// role-based resolution and avoids the name-collision mis-routing described in
// AD-4.
func firstPMResult(results []daemon.TrackerIssuesResult) (daemon.TrackerIssuesResult, bool) {
	for _, r := range results {
		if r.TrackerRole == "pm" {
			return r, true
		}
	}
	return daemon.TrackerIssuesResult{}, false
}
