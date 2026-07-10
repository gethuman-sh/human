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
	"github.com/gethuman-sh/human/internal/tracker"
)

// App is the Go backend bound into the webview via options.App.Bind. Every
// method here is callable from the TypeScript frontend. The app talks ONLY to
// the daemon client (daemon.GetTrackerIssues / daemon.BoardTransition /
// daemon.Subscribe) — never directly to a tracker or forge — so all credential
// handling, role resolution and the destructive-confirm bypass stay in the
// daemon, exactly as the TUI does it.
//
// The one exception is Instances() (instances.go), which discovers running
// Claude Code processes in-process via the monitor package. That path needs no
// credentials and the TUI runs the same monitor alongside its daemon calls, so
// it is consistent with the credential-only rationale above — and it cannot be
// a daemon route regardless, since monitor imports daemon (an import cycle).
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
	return boardFromResults(results, dockerAvailable()), nil
}

// CardsQuick fetches issue titles only — skipping the per-ticket comment scan
// that derives board stages — and places every open PM issue in the Backlog. It
// returns far faster than Cards(), so the board can render titles immediately;
// the subsequent Cards() call reconciles each card into its real stage. Docker
// availability is assumed here (the real value arrives with Cards()) so the quick
// path never blocks on a Docker round-trip.
func (a *App) CardsQuick() (BoardData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return BoardData{}, err
	}

	results, err := daemon.GetTrackerIssuesLite(info.Addr, info.Token)
	if err != nil {
		return BoardData{}, err
	}
	return boardFromResults(results, true), nil
}

// boardFromResults flattens the single PM-role result into the frontend card
// list. It is shared by Cards() (results carry derived BoardCards) and
// CardsQuick() (results carry titles only, so every non-hidden issue lands in
// Backlog). A PM issue with no derived card is hidden when its status is
// done/closed and placed in Backlog otherwise, mirroring daemon.DeriveBoardCard's
// marker-less decision so the quick pass and full pass agree on what to show.
func boardFromResults(results []daemon.TrackerIssuesResult, dockerAvailable bool) BoardData {
	data := BoardData{DockerAvailable: dockerAvailable}
	pm, ok := firstPMResult(results)
	if !ok {
		// No PM-role tracker configured yet: render five empty columns rather
		// than erroring, matching the "zero PM issues" requirement.
		return data
	}
	if pm.Err != "" {
		data.Error = pm.Err
	}

	for _, issue := range pm.Issues {
		card := pm.BoardCards[issue.Key]
		stage := card.Stage
		if stage == "" {
			// No derived card (quick fetch, or a marker-less ticket): a
			// done/closed ticket that never entered the pipeline is hidden;
			// everything else sits in Backlog.
			if issue.StatusType == tracker.CategoryDone || issue.StatusType == tracker.CategoryClosed {
				continue
			}
			stage = daemon.BoardBacklog
		}
		if stage == daemon.BoardHidden {
			// Closed PM ticket that never entered the pipeline — not shown.
			continue
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
	return data
}

// DaemonStatus reports whether the human daemon is currently reachable. The
// frontend polls this independently of Cards() because Cards() returns an
// error the instant the daemon is unreachable and stops there — the one case
// this indicator exists to show would otherwise never populate a "reachable"
// field. Combines IsReachable() (authoritative TCP dial, works across process
// namespaces e.g. host <-> devcontainer) with ReadAlivePid() (same-host
// PID-file liveness) so a daemon that is alive but momentarily not yet
// listening still reads as reachable, matching the TUI's dual-source check.
func (a *App) DaemonStatus() bool {
	info, err := daemon.ReadInfo()
	if err == nil && info.IsReachable() {
		return true
	}
	_, alive := daemon.ReadAlivePid()
	return alive
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

// GenerateFeatures asks the daemon to launch the human-features skill, which
// regenerates FEATURE.json. Like Transition it goes through the daemon so it
// runs the skill in the project's devcontainer — the same containerized agent
// path a kanban stage transition uses. It returns once the agent is launched,
// not when generation finishes; the pane polls Features() for the new file.
func (a *App) GenerateFeatures() error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.GenerateFeatures(info.Addr, info.Token)
}

// CloseTicket closes a PM ticket (transitions it to Done) via the daemon's
// dedicated close-ticket route. Like Transition it goes through the daemon, so
// the close is prompt-free — it never hits the interactive `issue status`
// confirmation. The board's own drag-and-confirm dialog is the user's consent.
func (a *App) CloseTicket(pmKey string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.CloseTicket(info.Addr, info.Token, daemon.CloseTicketRequest{PMKey: pmKey})
}

// IdeationMsg is the frontend-facing transcript entry.
type IdeationMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// IdeationView is the frontend-facing session snapshot.
type IdeationView struct {
	SessionID  string        `json:"sessionId,omitempty"`
	State      string        `json:"state"`
	Messages   []IdeationMsg `json:"messages"`
	CreatedKey string        `json:"createdKey,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// StartIdeation begins (or re-attaches to) the board ideation session.
func (a *App) StartIdeation(seed string, restart bool) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationStart(info.Addr, info.Token, daemon.IdeationStartRequest{Seed: seed, Restart: restart})
	if err != nil {
		return IdeationView{}, err
	}
	return ideationView(st), nil
}

// ReplyIdeation sends the user's answer into the running session.
func (a *App) ReplyIdeation(sessionID, message string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationReply(info.Addr, info.Token, daemon.IdeationReplyRequest{SessionID: sessionID, Message: message})
	if err != nil {
		return IdeationView{}, err
	}
	return ideationView(st), nil
}

// IdeationStatus returns the current session snapshot for panel polling and
// re-attach on panel reopen. Re-attach (rather than treating panel close as
// abandonment) is the deliberate AD-4 lifecycle: closing the panel does not
// stop the daemon-side session, so reopening must recover the live transcript.
func (a *App) IdeationStatus() (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.GetIdeationStatus(info.Addr, info.Token)
	if err != nil {
		return IdeationView{}, err
	}
	return ideationView(st), nil
}

// ideationView maps the daemon wire snapshot to the frontend-facing shape.
func ideationView(st daemon.IdeationStatus) IdeationView {
	view := IdeationView{
		SessionID:  st.SessionID,
		State:      string(st.State),
		Messages:   []IdeationMsg{},
		CreatedKey: st.CreatedKey,
		Error:      st.Error,
	}
	for _, m := range st.Transcript {
		view.Messages = append(view.Messages, IdeationMsg{Role: m.Role, Text: m.Text})
	}
	return view
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
