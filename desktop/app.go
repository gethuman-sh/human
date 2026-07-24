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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/boardprefs"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/ideaspace"
	"github.com/gethuman-sh/human/internal/pipeline"
	"github.com/gethuman-sh/human/internal/recentprojects"
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
	// recents holds the Projects Overview's most-recently-opened list. Same
	// local-file rationale as ideas: which projects were opened, and in what
	// order, is desktop-workspace state, never tracker or daemon state.
	recents *recentprojects.Store
	// prefs holds the board view preferences (per-column card order, hidden
	// tickets) — the same local-only rationale as ideas.
	prefs *boardprefs.Store
}

// NewApp constructs the backend. Wails injects the lifecycle context via
// startup, so there is nothing to wire here.
func NewApp() *App {
	return &App{
		ideas:   ideaspace.NewStore(ideaspace.DefaultPath()),
		recents: recentprojects.NewStore(recentprojects.DefaultPath()),
		prefs:   boardprefs.NewStore(boardprefs.DefaultPath()),
	}
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
	// StageEnteredAt is when the newest marker of the card's current stage
	// landed (RFC3339); the board's age badge renders how long the card has
	// been sitting. Empty when the card has no derived stage yet.
	StageEnteredAt string `json:"stageEnteredAt,omitempty"`
	// Labels and Description feed the Ideas→Backlog promotion: labels tell
	// the evolve session which idea labels to remove, the description seeds
	// the ideation conversation alongside the title.
	Labels      []string `json:"labels,omitempty"`
	Description string   `json:"description,omitempty"`
	// Assignee is the ticket owner shown in the detail panel. Display-only:
	// the board never assigns; empty renders as "Unassigned" in the frontend.
	Assignee string `json:"assignee,omitempty"`
	// Tracker/TrackerKind are the instance name and provider kind the issue
	// was listed from. The detail panel passes them back to GetIssueDetail so
	// the daemon resolves the exact instance — bare numeric keys are ambiguous
	// across kinds, and names can repeat across provider sections.
	Tracker     string `json:"tracker,omitempty"`
	TrackerKind string `json:"trackerKind,omitempty"`
	// IdeaColumn is the idea-space sub-column (0 loosest … 4 most concrete)
	// for cards in the Ideas stage. Locally persisted preference, never
	// tracker state; the zero value is the leftmost column, so an idea with
	// no saved placement starts loose by default.
	IdeaColumn int `json:"ideaColumn"`
	// Bug marks a defect ticket (bug label or bug issue type, see
	// tracker.Issue.IsBug). Bug cards render in the Bugs pane instead of the
	// workflow board's columns.
	Bug bool `json:"bug,omitempty"`
	// Hidden marks a ticket the user parked off the board (right-click →
	// Hide). Locally persisted view preference, never tracker state; the
	// frontend filters hidden cards out unless the user reveals them.
	Hidden bool `json:"hidden,omitempty"`
	// Options carries the card's open decision block: a stage ended in a fork
	// and a human must pick a direction. OptionsContext is the one-line why.
	Options        []daemon.BoardOption `json:"options,omitempty"`
	OptionsContext string               `json:"optionsContext,omitempty"`
	// MockupSlug/MockupState link the card to a locally generated mockup set:
	// "ready" once mockups/<slug>/index.json is valid, "creating" while a
	// launched generation has not produced it yet. Local file state — never
	// tracker state — so browsing or generating mocks leaves no trace on the
	// ticket.
	MockupSlug  string `json:"mockupSlug,omitempty"`
	MockupState string `json:"mockupState,omitempty"`
	// MockupChosenSlug/MockupChosenFile pin the ticket's chosen winner mockup
	// (a leaf group's slug + option file) when one has been marked, so the card
	// can surface that a design direction is selected and the viewer can
	// highlight the root→winner path. Empty when no winner is chosen.
	MockupChosenSlug string `json:"mockupChosenSlug,omitempty"`
	MockupChosenFile string `json:"mockupChosenFile,omitempty"`
}

// BoardData is the full payload the frontend renders: the flat card list plus an
// optional fetch error (surfaced as a banner) and a dockerAvailable flag the
// frontend uses to disable the agent-launching drop targets.
type BoardData struct {
	Cards           []Card `json:"cards"`
	Error           string `json:"error,omitempty"`
	DockerAvailable bool   `json:"dockerAvailable"`
	// ColumnOrder is the hand-sorted ticket order per queue column (top
	// first). The frontend sorts each column by it; cards absent from their
	// queue's list render after it in fetch order.
	ColumnOrder map[string][]string `json:"columnOrder,omitempty"`
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
		return BoardData{}, daemonCause(err)
	}
	data := boardFromResults(results, dockerAvailable(), a.ideas.Assignments(), cardMockups(), a.prefs.Snapshot())
	board.PrunePrefs(results,
		board.PruneTarget{Store: a.prefs, Keep: boardPrefsKeep(data)},
		board.PruneTarget{Store: a.ideas, Keep: ideaSpaceKeep(data)},
	)
	return data, nil
}

// daemonCause rewrites a daemon-client error for the Wails boundary: only
// err.Error() crosses to the frontend, and for daemon failures that is the
// generic "daemon command failed" wrapper. Folding in the cause chain and the
// daemon's stderr detail makes every board banner name what actually broke —
// an error surface must carry actionable information or not appear at all.
func daemonCause(err error) error {
	if err == nil {
		return nil
	}
	msg := errors.CauseChain(err)
	if stderr, ok := errors.AllDetails(err)["stderr"].(string); ok && strings.TrimSpace(stderr) != "" {
		msg += ": " + strings.TrimSpace(stderr)
	}
	return fmt.Errorf("%s", msg)
}

// IssueDetail is the full-ticket payload for the board's detail panel — only
// the fields the panel renders beyond what the card already carries.
// DescriptionHTML is rendered and sanitized by the daemon; the frontend may
// inject it verbatim.
type IssueDetail struct {
	Title           string `json:"title"`
	Assignee        string `json:"assignee,omitempty"`
	Description     string `json:"description,omitempty"`
	DescriptionHTML string `json:"descriptionHTML,omitempty"`
	// Comment-sourced sections the panel shows below the description, each
	// daemon-rendered to sanitized HTML so the frontend injects them verbatim.
	ReviewFindingsHTML string `json:"reviewFindingsHTML,omitempty"`
	FailureReasonHTML  string `json:"failureReasonHTML,omitempty"`
	FixSummaryHTML     string `json:"fixSummaryHTML,omitempty"`
}

// GetIssueDetail fetches one full ticket from the daemon. The detail panel
// calls it on open because list fetches on some trackers (e.g. Shortcut)
// return slim payloads without descriptions, so the card's own description
// can be empty even for a ticket that has one.
func (a *App) GetIssueDetail(trackerKind, trackerName, key string) (IssueDetail, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IssueDetail{}, err
	}
	issue, err := daemon.GetTrackerIssue(info.Addr, info.Token, trackerKind, trackerName, key)
	if err != nil {
		return IssueDetail{}, daemonCause(err)
	}
	return IssueDetail{
		Title:              issue.Title,
		Assignee:           issue.Assignee,
		Description:        issue.Description,
		DescriptionHTML:    issue.DescriptionHTML,
		ReviewFindingsHTML: issue.ReviewFindingsHTML,
		FailureReasonHTML:  issue.FailureReasonHTML,
		FixSummaryHTML:     issue.FixSummaryHTML,
	}, nil
}

// ideaSpaceKeep is the set of ticket keys still occupying the idea space
// (idea-stage cards). board.PrunePrefs drops idea placements outside it — but
// only on a trustworthy fetch (see board.CanPrune).
func ideaSpaceKeep(data BoardData) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, card := range data.Cards {
		if card.Stage == string(daemon.BoardIdeas) {
			keys[card.Key] = struct{}{}
		}
	}
	return keys
}

// SetIdeaColumn persists the idea-space placement for one ticket. Purely
// local UI state — never a tracker write or a board transition.
func (a *App) SetIdeaColumn(pmKey string, col int) error {
	return a.ideas.Set(pmKey, col)
}

// SetColumnOrder persists the hand-sorted card order for one queue column.
// Purely local UI state — never a tracker write or a board transition.
func (a *App) SetColumnOrder(queue string, keys []string) error {
	return a.prefs.SetOrder(queue, keys)
}

// SetCardHidden parks a ticket off the board (or restores it). Purely local
// UI state — the ticket on the tracker is untouched.
func (a *App) SetCardHidden(pmKey string, hidden bool) error {
	return a.prefs.SetHidden(pmKey, hidden)
}

// boardPrefsKeep is the set of every ticket key currently on the board.
// board.PrunePrefs drops order slots and hidden flags outside it — but only on a
// trustworthy fetch (see board.CanPrune).
func boardPrefsKeep(data BoardData) map[string]struct{} {
	keys := make(map[string]struct{}, len(data.Cards))
	for _, card := range data.Cards {
		keys[card.Key] = struct{}{}
	}
	return keys
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
		return BoardData{}, daemonCause(err)
	}
	return boardFromResults(results, true, a.ideas.Assignments(), cardMockups(), a.prefs.Snapshot()), nil
}

// boardFromResults flattens the single PM-role result into the frontend card
// list. It is shared by Cards() (results carry derived BoardCards) and
// CardsQuick() (results carry titles only, so every non-hidden issue lands in
// Backlog). A PM issue with no derived card is hidden when its status is
// done/closed and placed in Backlog otherwise, mirroring daemon.DeriveBoardCard's
// marker-less decision so the quick pass and full pass agree on what to show.
func boardFromResults(results []daemon.TrackerIssuesResult, dockerAvailable bool, ideaCols map[string]int, mocks map[string]cardMockupInfo, prefs boardprefs.Prefs) BoardData {
	data := BoardData{DockerAvailable: dockerAvailable, ColumnOrder: prefs.Columns}
	pm, ok := board.FirstPMResult(results)
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
		_, hidden := prefs.Hidden[issue.Key]
		data.Cards = append(data.Cards, Card{
			Key:              issue.Key,
			Title:            issue.Title,
			URL:              issue.URL,
			Stage:            string(stage),
			State:            string(card.State),
			EngineeringKey:   card.EngineeringKey,
			Branch:           card.Branch,
			PRURL:            card.PRURL,
			Error:            card.Error,
			Verdict:          card.Verdict,
			StageEnteredAt:   formatStageTime(card.StageEnteredAt),
			Labels:           issue.Labels,
			Description:      issue.Description,
			Assignee:         issue.Assignee,
			Tracker:          pm.TrackerName,
			TrackerKind:      pm.TrackerKind,
			Bug:              issue.IsBug(),
			Hidden:           hidden,
			IdeaColumn:       ideaCol,
			Options:          card.Options,
			OptionsContext:   card.OptionsContext,
			MockupSlug:       mock.Slug,
			MockupState:      mock.State,
			MockupChosenSlug: mock.ChosenSlug,
			MockupChosenFile: mock.ChosenFile,
		})
	}
	return data
}

// formatStageTime renders a marker time for the frontend, empty when the card
// has no derived stage timestamp (e.g. the quick-fetch path).
func formatStageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
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

// Doctor returns the daemon's substrate health checks for the rail LED. An
// unreachable daemon (or a daemon predating the doctor route) surfaces as an
// unhealthy result with a single explanatory check rather than an error, so
// the LED always has something truthful to show.
func (a *App) Doctor() daemon.DoctorData {
	info, err := daemon.ReadInfo()
	if err != nil || !info.IsReachable() {
		return daemon.DoctorData{Checks: []daemon.DoctorCheck{
			{ID: "daemon", Name: "daemon", OK: false, Detail: "not reachable — start it with 'human daemon'"},
		}}
	}
	data, err := daemon.GetDoctor(info.Addr, info.Token, false)
	if err != nil {
		return daemon.DoctorData{Checks: []daemon.DoctorCheck{
			{ID: "daemon", Name: "daemon", OK: false, Detail: "doctor unavailable: " + err.Error()},
		}}
	}
	return data
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
	return daemonCause(daemon.BoardTransition(info.Addr, info.Token, daemon.BoardTransitionRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
		From:    daemon.BoardStage(from),
		To:      daemon.BoardStage(to),
	}))
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
	return daemonCause(daemon.BoardFix(info.Addr, info.Token, daemon.BoardFixRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
	}))
}

// ChooseOption records the user's pick from a card's open decision block and
// relaunches the block's stage with the choice — the click on a choice the
// reviewer offered is the consent, exactly like a drag is for a transition.
func (a *App) ChooseOption(pmKey, optionID string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.SendBoardOption(info.Addr, info.Token, daemon.BoardOptionRequest{
		PMKey:    pmKey,
		OptionID: optionID,
	}))
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
	return daemonCause(daemon.GenerateFeatures(info.Addr, info.Token))
}

// FindBugs asks the daemon to launch the human-findbugs sweep for the registered
// project — the Bugs pane's Findbugs button. Like GenerateFeatures it goes
// through the daemon so the sweep runs containerized with the daemon's
// credentials, and it returns once the agent is launched; surviving findings
// surface as bug cards on the next Cards() reconcile.
func (a *App) FindBugs() error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.StartFindbugs(info.Addr, info.Token))
}

// FindbugsHunting reports whether a findbugs sweep is currently running for any
// registered project. It reads the sweep's own pipeline state file directly (the
// same project-local read pattern MockupSets uses), so the pane can show a live
// hunt indicator without a dedicated daemon route. A sweep sets status
// running/triaging for its whole run and cleans the file up at the end; a stale
// status older than findbugsHuntWindow (a crashed sweep) is treated as finished.
func (a *App) FindbugsHunting() bool {
	for _, p := range mockupRoots() {
		w := pipeline.Workspace{Dir: p.Dir, Name: "bugs"}
		status, err := w.StateGet("status")
		if err != nil || (status != "running" && status != "triaging") {
			continue
		}
		if fi, statErr := os.Stat(w.StatePath()); statErr == nil && time.Since(fi.ModTime()) < findbugsHuntWindow {
			return true
		}
	}
	return false
}

// findbugsHuntWindow bounds how long a running/triaging status counts as an
// active hunt; past it a crashed sweep no longer pins the pane's indicator.
const findbugsHuntWindow = 60 * time.Minute

// CreateMocks asks the daemon to launch the human-mockups skill for one PM
// ticket — the same containerized agent path as GenerateFeatures. It returns
// once the agent is launched; the card's mockupState reflects progress on the
// next Cards() reconcile.
func (a *App) CreateMocks(pmKey, pmTitle, description string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.CreateMocks(info.Addr, info.Token, daemon.CreateMocksRequest{
		PMKey:       pmKey,
		PMTitle:     pmTitle,
		Description: description,
	}))
}

// CreateVariations asks the daemon to spawn a new group of variations of one
// existing mockup (parentSlug/parentFile) honoring the free-text instructions.
// The source group is never touched; the new group attaches under it in the
// tree. Returns once the agent is launched, like CreateMocks.
func (a *App) CreateVariations(pmKey, feature, parentSlug, parentFile, instructions string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.CreateVariations(info.Addr, info.Token, daemon.CreateVariationsRequest{
		PMKey:        pmKey,
		Feature:      feature,
		ParentSlug:   parentSlug,
		ParentFile:   parentFile,
		Instructions: instructions,
	}))
}

// ChooseMockup marks a leaf mockup as the ticket's winner; an empty slug clears
// the current choice. Host-local state (never the tracker), consistent with the
// mockup link.
func (a *App) ChooseMockup(pmKey, slug, file string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.ChooseMockup(info.Addr, info.Token, daemon.ChooseMockupRequest{
		PMKey: pmKey,
		Slug:  slug,
		File:  file,
	}))
}

// PruneMockup archives a variation group and its descendants; the root group of
// a ticket cannot be pruned. If the current winner lives in the pruned subtree
// the daemon clears it.
func (a *App) PruneMockup(pmKey, slug string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.PruneMockup(info.Addr, info.Token, daemon.PruneMockupRequest{
		PMKey: pmKey,
		Slug:  slug,
	}))
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
	return daemonCause(daemon.CloseTicket(info.Addr, info.Token, daemon.CloseTicketRequest{PMKey: pmKey}))
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
		return IdeationView{}, daemonCause(err)
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
	return daemonCause(err)
}

// CreateBug files a defect ticket from the Bugs pane's `+` dialog. The daemon
// marks it as a bug the way the PM tracker natively understands, so the card
// lands in the bug grid on every backend.
func (a *App) CreateBug(title, description string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	_, err = daemon.BugCreate(info.Addr, info.Token, daemon.BugCreateRequest{Title: title, Description: description})
	return daemonCause(err)
}

// ReplyIdeation sends the user's answer into the running session.
func (a *App) ReplyIdeation(sessionID, message string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationReply(info.Addr, info.Token, daemon.IdeationReplyRequest{SessionID: sessionID, Message: message})
	if err != nil {
		return IdeationView{}, daemonCause(err)
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
		return IdeationView{}, daemonCause(err)
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
		return IdeationView{}, daemonCause(err)
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
