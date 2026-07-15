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
	"github.com/gethuman-sh/human/internal/ideaspace"
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
	// ideas holds the idea-space placement (ticket → sub-column). Local file
	// I/O rather than a daemon route: this is UI preference state that must
	// never touch the ticket, in line with the credential-only rationale above.
	ideas *ideaspace.Store
}

// NewApp constructs the backend. Wails injects the lifecycle context via
// startup, so there is nothing to wire here.
func NewApp() *App {
	return &App{ideas: ideaspace.NewStore(ideaspace.DefaultPath())}
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
	// Verdict is the latest review's verdict line; a failing verdict pins the
	// card in the Code lane with a warning instead of letting it advance.
	Verdict string `json:"verdict,omitempty"`
	// Labels and Description feed the Ideas→Backlog promotion: labels tell
	// the evolve session which idea labels to remove, the description seeds
	// the ideation conversation alongside the title.
	Labels      []string `json:"labels,omitempty"`
	Description string   `json:"description,omitempty"`
	// Assignee is the ticket owner shown in the detail panel. Display-only:
	// the board never assigns; empty renders as "Unassigned" in the frontend.
	Assignee string `json:"assignee,omitempty"`
	// Tracker is the instance name the issue was listed from. The detail
	// panel passes it back to IssueDetail so the daemon resolves the exact
	// instance — bare numeric keys are ambiguous across tracker kinds.
	Tracker string `json:"tracker,omitempty"`
	// IdeaColumn is the idea-space sub-column (0 loosest … 4 most concrete)
	// for cards in the Ideas stage. Locally persisted preference, never
	// tracker state; the zero value is the leftmost column, so an idea with
	// no saved placement starts loose by default.
	IdeaColumn int `json:"ideaColumn"`
	// Bug marks a defect ticket (bug label or bug issue type, see
	// tracker.Issue.IsBug). Bug cards render in the Bugs pane instead of the
	// workflow board's columns.
	Bug bool `json:"bug,omitempty"`
	// MockupSlug/MockupState link the card to a locally generated mockup set:
	// "ready" once mockups/<slug>/index.json is valid, "creating" while a
	// launched generation has not produced it yet. Local file state — never
	// tracker state — so browsing or generating mocks leaves no trace on the
	// ticket.
	MockupSlug  string `json:"mockupSlug,omitempty"`
	MockupState string `json:"mockupState,omitempty"`
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
	data := boardFromResults(results, dockerAvailable(), a.ideas.Assignments(), cardMockups())
	a.pruneIdeaSpace(data)
	return data, nil
}

// IssueDetail is the full-ticket payload for the board's detail panel — only
// the fields the panel renders beyond what the card already carries.
type IssueDetail struct {
	Title       string `json:"title"`
	Assignee    string `json:"assignee,omitempty"`
	Description string `json:"description,omitempty"`
}

// GetIssueDetail fetches one full ticket from the daemon. The detail panel
// calls it on open because list fetches on some trackers (e.g. Shortcut)
// return slim payloads without descriptions, so the card's own description
// can be empty even for a ticket that has one.
func (a *App) GetIssueDetail(trackerName, key string) (IssueDetail, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IssueDetail{}, err
	}
	issue, err := daemon.GetTrackerIssue(info.Addr, info.Token, trackerName, key)
	if err != nil {
		return IssueDetail{}, err
	}
	return IssueDetail{
		Title:       issue.Title,
		Assignee:    issue.Assignee,
		Description: issue.Description,
	}, nil
}

// pruneIdeaSpace drops idea-space placements for tickets that are no longer
// idea cards (promoted or closed). Only a fully successful full fetch may
// prune — a transient tracker error must not wipe saved placements — and a
// prune failure is ignored: a stale entry is harmless, the board renders from
// current idea cards regardless.
func (a *App) pruneIdeaSpace(data BoardData) {
	if data.Error != "" {
		return
	}
	keys := make(map[string]struct{})
	for _, card := range data.Cards {
		if card.Stage == string(daemon.BoardIdeas) {
			keys[card.Key] = struct{}{}
		}
	}
	_ = a.ideas.PruneExcept(keys)
}

// SetIdeaColumn persists the idea-space placement for one ticket. Purely
// local UI state — never a tracker write or a board transition.
func (a *App) SetIdeaColumn(pmKey string, col int) error {
	return a.ideas.Set(pmKey, col)
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
	return boardFromResults(results, true, a.ideas.Assignments(), cardMockups()), nil
}

// boardFromResults flattens the single PM-role result into the frontend card
// list. It is shared by Cards() (results carry derived BoardCards) and
// CardsQuick() (results carry titles only, so every non-hidden issue lands in
// Backlog). A PM issue with no derived card is hidden when its status is
// done/closed and placed in Backlog otherwise, mirroring daemon.DeriveBoardCard's
// marker-less decision so the quick pass and full pass agree on what to show.
func boardFromResults(results []daemon.TrackerIssuesResult, dockerAvailable bool, ideaCols map[string]int, mocks map[string]cardMockupInfo) BoardData {
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
			// ideas sit in the Ideas column by their label alone; everything
			// else sits in Backlog. Mirrors daemon.DeriveBoardCard so the
			// quick pass and full pass agree.
			if issue.StatusType == tracker.CategoryDone || issue.StatusType == tracker.CategoryClosed {
				continue
			}
			if issue.IsIdea() {
				stage = daemon.BoardIdeas
			} else {
				stage = daemon.BoardBacklog
			}
		}
		if stage == daemon.BoardHidden {
			// Closed PM ticket that never entered the pipeline — not shown.
			continue
		}
		ideaCol := 0
		if stage == daemon.BoardIdeas {
			// Missing key → zero value → leftmost column, the loose default.
			ideaCol = ideaCols[issue.Key]
		}
		mock := mocks[issue.Key]
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
			Verdict:        card.Verdict,
			Labels:         issue.Labels,
			Description:    issue.Description,
			Assignee:       issue.Assignee,
			Tracker:        pm.TrackerName,
			Bug:            issue.IsBug(),
			IdeaColumn:     ideaCol,
			MockupSlug:     mock.Slug,
			MockupState:    mock.State,
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

// FixBug asks the daemon to launch the autonomous bug-fix pipeline
// (/human-autofix) on a bug ticket — the Bugs pane's Fix drop. Like Transition
// it goes through the daemon so the agent runs containerized with the daemon's
// credentials; the daemon guards against double-launches, so an optimistic
// re-drop is safe.
func (a *App) FixBug(pmKey, pmTitle string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.BoardFix(info.Addr, info.Token, daemon.BoardFixRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
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

// CreateMocks asks the daemon to launch the human-mockups skill for one PM
// ticket — the same containerized agent path as GenerateFeatures. It returns
// once the agent is launched; the card's mockupState reflects progress on the
// next Cards() reconcile.
func (a *App) CreateMocks(pmKey, pmTitle, description string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.CreateMocks(info.Addr, info.Token, daemon.CreateMocksRequest{
		PMKey:       pmKey,
		PMTitle:     pmTitle,
		Description: description,
	})
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

// IdeationOption is one guided-mode multiple-choice question, frontend-facing.
type IdeationOption struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Kind    string   `json:"kind"`
}

// IdeationDraftView is the frontend-facing agent-drafted ticket summary.
type IdeationDraftView struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// IdeationView is the frontend-facing session snapshot.
type IdeationView struct {
	SessionID  string             `json:"sessionId,omitempty"`
	Mode       string             `json:"mode,omitempty"`
	State      string             `json:"state"`
	Messages   []IdeationMsg      `json:"messages"`
	Question   *IdeationOption    `json:"question,omitempty"`
	Draft      *IdeationDraftView `json:"draft,omitempty"`
	CreatedKey string             `json:"createdKey,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// StartIdeation begins (or re-attaches to) the board ideation session. mode
// is "chat" or "guided"; empty defaults to "chat" in the daemon engine.
// evolveKey (with the card's idea labels) switches the session to evolve
// mode: the outcome rewrites that ticket in place instead of creating one —
// the Ideas→Backlog promotion path.
func (a *App) StartIdeation(seed, mode string, restart bool, evolveKey string, evolveLabels []string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationStart(info.Addr, info.Token, daemon.IdeationStartRequest{
		Seed:         seed,
		Mode:         daemon.IdeationMode(mode),
		Restart:      restart,
		EvolveKey:    evolveKey,
		EvolveLabels: evolveLabels,
	})
	if err != nil {
		return IdeationView{}, err
	}
	return ideationView(st), nil
}

// CreateIdea quick-captures a title-only idea ticket — the Ideas column's `+`.
func (a *App) CreateIdea(title string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	_, err = daemon.IdeaCreate(info.Addr, info.Token, daemon.IdeaCreateRequest{Title: title})
	return err
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

// ApproveIdeation submits the user's (possibly edited) guided-mode draft for
// ticket creation.
func (a *App) ApproveIdeation(sessionID, title, description string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationApprove(info.Addr, info.Token, daemon.IdeationApproveRequest{
		SessionID:   sessionID,
		Title:       title,
		Description: description,
	})
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
		Mode:       string(st.Mode),
		State:      string(st.State),
		Messages:   []IdeationMsg{},
		CreatedKey: st.CreatedKey,
		Error:      st.Error,
	}
	if st.Question != nil {
		view.Question = &IdeationOption{Text: st.Question.Text, Options: st.Question.Options, Kind: st.Question.Kind}
	}
	if st.Draft != nil {
		view.Draft = &IdeationDraftView{Title: st.Draft.Title, Description: st.Draft.Description}
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
