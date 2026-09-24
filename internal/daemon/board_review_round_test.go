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

// sc5395History is SC-5395's real marker thread up to the late handoff
// (2026-09-24 10:07 → 10:18, board markers only, at their real times): a fix
// run that handed off, reviewed itself, was dropped on Deploy, and then
// re-posted its handoff to record the commit the reviewer had made on the
// branch. Every commit that handoff names is one the verdict judged.
func sc5395History() []tracker.Comment {
	at := func(s string) time.Time {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return t
	}
	handoff := func(commits string) string {
		return ReadyForReviewHeader + "\nbranch: autofix/sc-5395\ncommits: " + commits
	}
	return []tracker.Comment{
		cmt(ImplementationStartedHeader, at("2026-09-24T09:40:00Z")),
		cmt(handoff("f93dc92c"), at("2026-09-24T10:07:20Z")),
		cmt(ReviewStartedHeader, at("2026-09-24T10:07:23Z")),
		cmt(ReviewCompleteHeader+"\nverdict: pass with notes\ncommits: f93dc92c, 9a0bf0ea",
			at("2026-09-24T10:14:28Z")),
		cmt(PRReviewStartedHeader+"\npr: https://github.com/gethuman-sh/human/pull/566\nnumber: 566\nbranch: autofix/sc-5395",
			at("2026-09-24T10:15:54Z")),
		cmt(handoff("f93dc92c, 9a0bf0ea"), at("2026-09-24T10:18:11Z")),
	}
}

// A handoff re-posted only to record what the branch already holds names
// nothing the verdict has not already judged, so it is not a new round: the
// verdict keeps governing and no second review is due (SC-5475).
func TestHandoffAwaitsReview_AHandoffNamingOnlyJudgedCommitsIsNoNewRound(t *testing.T) {
	comments := []tracker.Comment{
		cmt(ReviewCompleteHeader+"\nverdict: pass\ncommits: f93dc92c", time.Unix(1, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: autofix/sc-5395\ncommits: f93dc92c", time.Unix(2, 0)),
	}

	assert.False(t, handoffAwaitsReview(comments))
	assert.Equal(t, "pass", currentVerdict(comments))
}

// SC-5395's real thread: the late handoff was posted while the machine PR
// review was live. It must not blank the passing verdict, revert the card out
// of the done stage, or give the recovery sweep anything to chain — the PR
// review already in flight is judging these exact commits.
func TestDeriveBoardCard_LateHandoffLeavesTheLivePRReviewAlone(t *testing.T) {
	comments := sc5395History()

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Equal(t, DeployPhasePRReview, card.DeployPhase)
	assert.Equal(t, "pass with notes", card.Verdict)

	cards := []ReconcileCard{{Key: "SC-5395", Comments: comments}}
	var chained []string
	n := reconcileOrphanedHandoffs(reviewSet(cards, alwaysReachable), ReconcileDeps{
		ChainReview: func(pmKey string) error { chained = append(chained, pmKey); return nil },
	}, time.Now())
	assert.Equal(t, 0, n)
	assert.Empty(t, chained)
}

// The SC-4958 guard: a handoff that genuinely hands back a rebuild — naming a
// commit the failing verdict never judged — must still chain exactly one
// fresh review. The commit test must narrow the old recency test, never
// replace it with something that stops firing on real rework.
func TestHandleBoardAgentExit_AReworkHandoffWithNewCommitsStillChainsOneReview(t *testing.T) {
	c := &syncCommenter{comments: []tracker.Comment{
		cmt(ReadyForReviewHeader+"\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
		cmt(ReviewStartedHeader, time.Unix(2, 0)),
		cmt(ReviewCompleteHeader+"\nverdict: fail\ncommits: abc123", time.Unix(3, 0)),
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

	assert.Equal(t, 1, chained, "a rework handoff naming a commit the verdict never judged must chain a fresh review")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.added, "chaining a review posts no marker of its own")
}

// An approval is evidence about the revision it judged. A handoff re-posted
// afterward to record what the branch already holds must not void it — only a
// handoff naming work the verdict never judged is a real new round (SC-5475).
func TestCurrentApproval_SurvivesAHandoffThatHandsOverNothingUnjudged(t *testing.T) {
	const head = "df4beda7f8cf2371c60ecb892b7864247740e1ff"
	comments := []tracker.Comment{
		cmt(ReviewCompleteHeader+"\nverdict: pass with notes\ncommits: f93dc92c, 9a0bf0ea", time.Unix(1, 0)),
		cmt(PRReviewPassedHeader+"\nbranch: autofix/sc-5395\nhead: "+head, time.Unix(2, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: autofix/sc-5395\ncommits: f93dc92c, 9a0bf0ea", time.Unix(3, 0)),
	}

	got, ok := currentApproval(comments, "autofix/sc-5395")
	require.True(t, ok)
	assert.Equal(t, head, got)

	// The negative half: a handoff naming a THIRD, unjudged commit is a real new
	// round and must still void the approval.
	comments[2] = cmt(ReadyForReviewHeader+"\nbranch: autofix/sc-5395\ncommits: f93dc92c, 9a0bf0ea, 1234abcd", time.Unix(3, 0))
	_, ok = currentApproval(comments, "autofix/sc-5395")
	assert.False(t, ok)
}

// The done stage's branch resolution must not revert to an earlier round's
// handoff branch just because that handoff was re-posted after a deploy
// override — the re-post names nothing the verdict did not already judge.
func TestDoneStageBranch_ARepostedHandoffDoesNotRevertTheDeploysBranch(t *testing.T) {
	comments := []tracker.Comment{
		cmt(ReviewCompleteHeader+"\nverdict: pass\ncommits: f93dc92c", time.Unix(1, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: autofix/old\ncommits: f93dc92c", time.Unix(2, 0)),
		cmt(DeployStartedHeader+"\nbranch: autofix/override", time.Unix(3, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: autofix/old\ncommits: f93dc92c", time.Unix(4, 0)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "autofix/override", doneStageBranch(comments, card))
}

// An ordinary rework round must still outrank a stale PR-loop start marker
// from a round the ticket has already moved past, even though the review that
// judges the rework names EXACTLY the handoff's commits — which every current
// prompt does on a pass. handoffNamesUnjudgedCommit alone cannot tell this
// apart from a bookkeeping repost, because both end with a handoff naming
// what the verdict judged; only handoff-precedes-verdict order does
// (SC-5475 follow-up — this thread regressed doneStageBranch to "feat/a").
func TestDoneStageBranch_AReworkHandoffJudgedByATimelyVerdictStillOutranksAStaleStartMarker(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader+"\npr: u\nnumber: 7\nbranch: feat/a", time.Unix(1, 0)),
		cmt(ImplementationStartedHeader, time.Unix(2, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: feat/b\ncommits: def456", time.Unix(3, 0)),
		cmt(ReviewStartedHeader, time.Unix(4, 0)),
		cmt(ReviewCompleteHeader+"\nverdict: pass\ncommits: def456", time.Unix(5, 0)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, "feat/b", doneStageBranch(comments, card))
}

// The currentApproval mirror of the test above: a rework round's handoff,
// judged by a verdict posted after it and naming exactly its commits, must
// still void a stale approval from the round the ticket has moved past.
func TestCurrentApproval_AReworkHandoffJudgedByATimelyVerdictStillVoidsAStaleApproval(t *testing.T) {
	const head = "df4beda7f8cf2371c60ecb892b7864247740e1ff"
	comments := []tracker.Comment{
		cmt(PRReviewPassedHeader+"\nbranch: feat/a\nhead: "+head, time.Unix(1, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: feat/b\ncommits: def456", time.Unix(2, 0)),
		cmt(ReviewCompleteHeader+"\nverdict: pass\ncommits: def456", time.Unix(3, 0)),
	}

	_, ok := currentApproval(comments, "feat/a")
	assert.False(t, ok)
}
