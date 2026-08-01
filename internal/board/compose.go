package board

import (
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Compose flattens a tracker fetch into the board the frontend renders: the
// single PM-role result joined with its derived BoardCards, plus the structural
// notices that explain an empty or partial board.
//
// It deliberately produces only what is TRUE OF THE PROJECT — the same board for
// anyone looking at it. Three per-card fields are left at their zero values
// because they are true only of the person looking: IdeaColumn, Hidden, and the
// Mockup* set. The viewer fills those in (desktop applyLocal), which is why this
// can run on the daemon and be shared, and why hidden cards are still returned
// here: hiding is a viewer's filter, not a property of the work.
//
// dockerAvailable belongs to the machine that launches agents, so the caller
// supplies it rather than probing — on the daemon that is its own host, which is
// the host that actually matters.
func Compose(results []daemon.TrackerIssuesResult, dockerAvailable bool) daemon.BoardView {
	view := daemon.BoardView{DockerAvailable: dockerAvailable}
	pm, ok := FirstPMResult(results)
	if !ok {
		// No PM-role tracker resolved: rather than five silently empty columns
		// that read as "no work" (SC-1655), surface an explicit notice telling
		// the user a tracker needs role: pm to appear on the board.
		view.Notice = PMRoleNotice(results)
		return view
	}
	if pm.Err != "" {
		view.Error = pm.Err
	}
	// A capped fetch renders a full board that silently omits the overflow; the
	// affordance tells the user the list is partial (and that their saved state
	// is preserved because pruning paused). See CanPrune / SC-1693.
	view.Truncation = TruncationNotice(results)

	blockedBy := map[string][]string{}
	for _, issue := range pm.Issues {
		card := pm.BoardCards[issue.Key]
		stage, include := composedStage(issue, card)
		if !include {
			continue
		}
		blockedBy[issue.Key] = issue.BlockedBy()
		view.Cards = append(view.Cards, daemon.BoardViewCard{
			Key:            issue.Key,
			Title:          issue.Title,
			URL:            issue.URL,
			Stage:          string(stage),
			State:          string(card.State),
			Degraded:       card.Degraded,
			EngineeringKey: card.EngineeringKey,
			Branch:         card.Branch,
			PRURL:          card.PRURL,
			Error:          card.Error,
			Verdict:        card.Verdict,
			StageEnteredAt: formatStageTime(card.StageEnteredAt),
			DeployPhase:    card.DeployPhase,
			Labels:         issue.Labels,
			Description:    issue.Description,
			Assignee:       issue.Assignee,
			Tracker:        pm.TrackerName,
			TrackerKind:    pm.TrackerKind,
			Bug:            issue.IsBug(),
			Security:       issue.IsSecurity(),
			Options:        card.Options,
			OptionsContext: card.OptionsContext,
		})
	}
	markBlocked(view.Cards, blockedBy)
	return view
}

// markBlocked badges each card with the blockers that are still on the board.
//
// Presence on the board IS the test for "still open" here: a finished blocker
// left the board when it was closed, so it drops out without a second fetch.
// That makes the badge approximate — a blocker the fetch never returned, say on
// another tracker, reads as finished — and approximate is the right trade for a
// label. Nothing acts on this; the launch gate resolves real status before it
// holds any work back.
func markBlocked(cards []daemon.BoardViewCard, blockedBy map[string][]string) {
	onBoard := make(map[string]bool, len(cards))
	for _, c := range cards {
		onBoard[c.Key] = true
	}
	for i, c := range cards {
		for _, key := range blockedBy[c.Key] {
			if onBoard[key] {
				cards[i].Blockers = append(cards[i].Blockers, key)
			}
		}
	}
}

// composedStage resolves the column a ticket sits in, and whether it belongs on
// the board at all. It keeps the two placement rules that differ only in why
// they exist:
//
//   - A DEGRADED card's markers could not be read this scan, so it is pinned to
//     its last-known column (Backlog when there is no prior stage) rather than
//     appearing as idle, actionable Backlog work (1700).
//   - A card with no derived stage never entered the pipeline: a done/closed
//     ticket is hidden, an idea sits in Ideas by its label alone, everything
//     else lands in Backlog. Mirrors daemon.DeriveBoardCard so the quick pass
//     and the full pass agree.
//
// An OUTAGE card (State == "outage", SC-2307) needs no special case here: unlike
// a DEGRADED card it has a genuine derived stage, so it flows through the normal
// path and its state is forwarded verbatim (State: string(card.State) below) for
// the frontend to render as "machine down".
func composedStage(issue tracker.Issue, card daemon.BoardCard) (daemon.BoardStage, bool) {
	if card.Degraded {
		stage := card.Stage
		if stage == "" || stage == daemon.BoardHidden {
			stage = daemon.BoardBacklog
		}
		return stage, true
	}
	stage := card.Stage
	if stage == "" {
		if issue.StatusType == tracker.CategoryDone || issue.StatusType == tracker.CategoryClosed {
			return "", false
		}
		if issue.IsIdea() {
			stage = daemon.BoardIdeas
		} else {
			stage = daemon.BoardBacklog
		}
	}
	if stage == daemon.BoardHidden {
		// Closed PM ticket that never entered the pipeline — not shown.
		return "", false
	}
	return stage, true
}

// formatStageTime renders a marker time for the frontend, empty when the card
// has no derived stage yet.
func formatStageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
