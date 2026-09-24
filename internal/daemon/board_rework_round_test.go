package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/tracker"
)

// sc4923History is SC-4923's real marker thread (2026-09-14 09:39 → 09-15 07:22),
// board markers only, at their real times: one plan, a build, a review that
// returned fail with a decision block, and three rework rounds each ending in a
// handoff nothing ever reviewed. It is the history this ticket was filed from.
func sc4923History() []tracker.Comment {
	at := func(s string) time.Time {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return t
	}
	handoff := func(commits string) string {
		return ReadyForReviewHeader + "\nbranch: sc-4923-recreate-description\ncommits: " + commits
	}
	return []tracker.Comment{
		cmt(PlanningStartedHeader, at("2026-09-14T09:39:00Z")),
		cmt(PlanReadyHeader, at("2026-09-14T09:50:00Z")),
		cmt(ImplementationStartedHeader, at("2026-09-14T09:52:00Z")),
		cmt(handoff("a9249df8, 6013f8de"), at("2026-09-14T10:04:00Z")),
		cmt(ReviewStartedHeader, at("2026-09-14T10:06:00Z")),
		cmt(ReviewCompleteHeader+"\nverdict: fail", at("2026-09-14T10:14:00Z")),
		cmt("[human:options]\nstage: implementation\n1: fix the finding\n2: ship as is", at("2026-09-14T10:14:30Z")),
		cmt("[human:option-chosen]\nstage: implementation\nchosen: 1", at("2026-09-14T12:15:00Z")),
		cmt(ImplementationStartedHeader, at("2026-09-14T12:15:10Z")),
		cmt(handoff("37aaba5d, cc9da9de"), at("2026-09-14T12:24:00Z")),
		cmt(ImplementationStartedHeader, at("2026-09-14T12:39:00Z")),
		cmt(handoff("37aaba5d, cc9da9de"), at("2026-09-14T12:45:00Z")),
		cmt(ImplementationStartedHeader, at("2026-09-15T07:17:00Z")),
		cmt(handoff("37aaba5d, cc9da9de"), at("2026-09-15T07:22:00Z")),
	}
}

// A verdict judges the round it read. A handoff posted after it answers it, so
// the card is a built card awaiting review — not a finished review carrying a
// verdict 21 hours older than the newest marker on the ticket.
func TestDeriveBoardCard_HandoffNewerThanTheVerdictRetiresIt(t *testing.T) {
	card := DeriveBoardCard(sc4923History(), tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardDone, card.State)
	assert.Empty(t, card.Verdict, "a verdict older than the newest handoff no longer governs the card")
	assert.Equal(t, "sc-4923-recreate-description", card.Branch)
	assert.Equal(t, "37aaba5d, cc9da9de", card.Commits)
}

// The first rework handoff is enough — the card leaves the verification lane the
// moment the rebuild is handed back, not only after three rounds of it.
func TestDeriveBoardCard_FirstReworkHandoffAlreadyAwaitsReview(t *testing.T) {
	card := DeriveBoardCard(sc4923History()[:10], tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardDone, card.State)
	assert.Empty(t, card.Verdict)
}

// While the rework RUNS the verdict still stands: it is what dispatched the run,
// what AgentNamesForCard reads to name the implementation agent, and what the
// board badges "fixing…". Only the handoff answering it retires it.
func TestDeriveBoardCard_AVerdictStandsUntilTheReworkHandsBack(t *testing.T) {
	card := DeriveBoardCard(sc4923History()[:9], tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardVerification, card.Stage)
	assert.Equal(t, BoardDone, card.State)
	assert.Equal(t, "fail", card.Verdict)
}

// The durable half of the fix: the recovery sweep that exists for a handoff whose
// review never launched must reach a SECOND-round handoff too. This is the card
// that must unstick on a daemon restart with no manual marker.
func TestReconcileOrphanedHandoffs_RecoversAHandoffPostedAfterAVerdict(t *testing.T) {
	cards := []ReconcileCard{{Key: "SC-4923", Comments: sc4923History()}}
	var chained []string

	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		ChainReview: func(pmKey string) error { chained = append(chained, pmKey); return nil },
	}, time.Now())

	require.Equal(t, 1, n)
	assert.Equal(t, []string{"SC-4923"}, chained)
}

// The live half: a clean build whose handoff is newer than the round-1 verdict
// must flow through to the chain. The SC-782 guard it passes through protects an
// in-container review NEWER than the handoff, which this is not.
func TestHandleBoardAgentExit_ReworkHandoffChainsAFreshReview(t *testing.T) {
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ReadyForReviewHeader+"\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
		cmt(ReviewStartedHeader, time.Unix(2, 0)),
		cmt(ReviewCompleteHeader+"\nverdict: fail", time.Unix(3, 0)),
		cmt(ImplementationStartedHeader, time.Unix(4, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: feat/x\ncommits: def456", time.Unix(5, 0)),
	}}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	chained := 0

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-implementation"},
		FailureDeps{
			CommenterFor: commenterFor,
			ChainReview:  func(string) error { chained++; return nil },
			Reachable:    alwaysReachable,
			Logger:       zerolog.Nop(),
		})

	assert.Equal(t, 1, chained, "a rework handoff newer than the verdict must chain a fresh review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "chaining a review posts no marker of its own")
}
