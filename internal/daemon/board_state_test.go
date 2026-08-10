package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

func cmt(body string, t time.Time) tracker.Comment {
	return tracker.Comment{Body: body, Created: t}
}

// The PR review→fix loop lives inside the done (deploy) stage: its markers must
// derive the done stage — leaving the verification→done transition adjacency
// untouched — while still exposing the loop's running/failed state (SC-1387).
func TestDeriveBoardCard_prReviewLoop(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("pr-review-started is done stage running", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt(PRReviewStartedHeader, t0)}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("pr-fix-started is done stage running", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt(PRFixStartedHeader, t0)}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("pr-review-failed reds the card with a reason", func(t *testing.T) {
		card := DeriveBoardCard(
			[]tracker.Comment{cmt(PRReviewFailedHeader+"\nreview budget exhausted — needs a human", t0)},
			tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardFailed, card.State)
		assert.NotEmpty(t, card.Error)
	})

	t.Run("pr-review supersedes a passed verification (furthest stage wins)", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(ReviewCompleteHeader+"\nverdict: pass", t0),
			cmt(PRReviewStartedHeader, t1),
		}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("a later deployed marker retires the loop state (latest wins)", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(PRReviewStartedHeader, t0),
			cmt(DeployedHeader, t1),
		}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardDone, card.State)
	})
}

// SC-3156 (criterion 5): a card reddened by a [human:review-failed] recovers when
// the review goes on to finish — a strictly-newer [human:review-complete] is the
// latest verification marker and supersedes the failure (supersededByNewerMarker
// + latest-wins), so the card leaves red without any bespoke recovery machinery.
func TestDeriveBoardCard_ReviewCompleteSupersedesReviewFailed(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:ready-for-review]\nbranch: feat/x\ncommits: abc123", time.Unix(1, 0)),
		cmt(ReviewStartedHeader, time.Unix(2, 0)),
		cmt(ReviewFailedHeader+"\nagent died", time.Unix(3, 0)),
		cmt(ReviewCompleteHeader+"\nverdict: pass", time.Unix(4, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.NotEqual(t, BoardFailed, card.State, "a later review-complete must clear the review-failed red")
	assert.Equal(t, "pass", card.Verdict, "the recovered card carries the review verdict")
}

func TestDeriveBoardCard(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)

	t.Run("no markers open is backlog", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardBacklog, card.Stage)
		assert.Equal(t, BoardIdle, card.State)
	})

	t.Run("no markers closed is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryClosed, false)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("no markers done is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryDone, false)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("done with markers is hidden", func(t *testing.T) {
		// A ticket closed mid-pipeline (board Close action, or directly on the
		// tracker) has left the board — its marker history must not keep it
		// rendered in a column.
		comments := []tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-7", t0),
			cmt("[human:implementation-started]", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryDone, false)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("closed with markers is hidden", func(t *testing.T) {
		comments := []tracker.Comment{cmt("[human:planning-started]", t0)}
		card := DeriveBoardCard(comments, tracker.CategoryClosed, false)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("planning-started is planning running", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:planning-started]", t0)}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("plan-ready with eng key", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-7", t0)}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "HUM-7", card.EngineeringKey)
	})

	t.Run("furthest stage wins", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-7", t0),
			cmt("[human:implementation-started]", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
		assert.Equal(t, "HUM-7", card.EngineeringKey)
	})

	t.Run("latest within stage supersedes", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:planning-started]", t0),
			cmt("[human:plan-ready]\nengineering: HUM-9", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardDone, card.State)
	})

	t.Run("ready-for-review carries branch and eng", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x\ncommits: abc", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "feat/x", card.Branch)
		assert.Equal(t, "HUM-9", card.EngineeringKey)
		// SC-695: the handoff commits must ride the card so the daemon can bind
		// the reviewer to the exact SHAs handed off, not the reviewed HEAD.
		assert.Equal(t, "abc", card.Commits)
	})

	t.Run("implementation-failed records error", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:implementation-failed]\ncompile error in foo.go", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardFailed, card.State)
		// The human-readable reason, not the marker header line.
		assert.Equal(t, "compile error in foo.go", card.Error)
	})

	t.Run("diagnosed failure keeps card error to the headline", func(t *testing.T) {
		// SC-620: the marker body is headline-first, then a markdown detail
		// block; the card's one-line error must stay exactly the headline.
		body := "[human:implementation-failed]\nclaude exited with code 1: API Error\n\nagent: board-SC-1-implementation\n\nlast output:\n~~~\nboom\n~~~"
		card := DeriveBoardCard([]tracker.Comment{cmt(body, t0)}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardFailed, card.State)
		assert.Equal(t, "claude exited with code 1: API Error", card.Error)
	})

	t.Run("SC-910 newer marker in earlier stage supersedes furthest-stage failure", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:deploy-failed]\nmerge conflict on main", t0),
			cmt("[human:implementation-started]", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.NotEqual(t, BoardFailed, card.State)
		assert.Empty(t, card.Error)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("SC-910 lone failure with no newer marker still reds", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:deploy-failed]\nmerge conflict on main", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardFailed, card.State)
		assert.Equal(t, "merge conflict on main", card.Error)
	})

	t.Run("SC-910 genuine failure as newest marker stays failed", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:implementation-started]", t0),
			cmt("[human:implementation-failed]\ncompile error", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardFailed, card.State)
		assert.Equal(t, "compile error", card.Error)
	})

	t.Run("SC-910 deploy-failed carrying a pr line exposes PRURL", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:deploy-failed]\nmerge conflict on main\npr: https://github.com/o/r/pull/7", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardFailed, card.State)
		assert.Equal(t, "https://github.com/o/r/pull/7", card.PRURL)
	})

	t.Run("full chain ending pr-pushed", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-9", t0),
			cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", t1),
			cmt("[human:review-complete]", t2),
			cmt("[human:pr-pushed]\npr: https://example/pr/1", t2.Add(time.Second)),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "https://example/pr/1", card.PRURL)
		assert.Equal(t, "feat/x", card.Branch)
		assert.Equal(t, "HUM-9", card.EngineeringKey)
	})
}

func TestFailureBody(t *testing.T) {
	t.Run("full diagnosis returned without the header", func(t *testing.T) {
		body := "[human:planning-failed]\nheadline here\n\ndetail block\n~~~\ntail\n~~~"
		assert.Equal(t, "headline here\n\ndetail block\n~~~\ntail\n~~~", failureBody(body))
	})
	t.Run("headline-only marker returns the headline", func(t *testing.T) {
		assert.Equal(t, "just a reason", failureBody("[human:planning-failed]\njust a reason"))
	})
	t.Run("header-only marker falls back to the header", func(t *testing.T) {
		assert.Equal(t, "[human:planning-failed]", failureBody("[human:planning-failed]"))
	})
}

// SC-1701: two classified markers for one stage sharing a one-second Created
// time must resolve on the monotonic comment ID (higher ID = newer), not on the
// order the tracker returned the slice in. review-failed(1680) then
// review-started(1681) at the same second: 1681 is genuinely newer, so both
// orderings must derive running.
func TestDeriveBoardCard_sameSecondTieBreaksOnID(t *testing.T) {
	tie := time.Date(2026, 7, 29, 11, 59, 53, 0, time.UTC)
	failed := tracker.Comment{ID: "1680", Body: ReviewFailedHeader + "\nreview failed", Created: tie}
	started := tracker.Comment{ID: "1681", Body: ReviewStartedHeader, Created: tie}

	forward := DeriveBoardCard([]tracker.Comment{failed, started}, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardVerification, forward.Stage)
	assert.Equal(t, BoardRunning, forward.State)

	reversed := DeriveBoardCard([]tracker.Comment{started, failed}, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardVerification, reversed.Stage)
	assert.Equal(t, BoardRunning, reversed.State)
}

func TestDeriveBoardCard_HasPlan(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("plan comment sets HasPlan without shifting stage", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan]\n\n## Steps\n1. do it", t0)}, tracker.CategoryUnstarted, false)
		assert.True(t, card.HasPlan)
		// The plan is content, not a stage signal — the card stays in Backlog.
		assert.Equal(t, BoardBacklog, card.Stage)
	})

	t.Run("plan-ready is not a plan comment", func(t *testing.T) {
		// Prefix isolation: [human:plan-ready] must not read as [human:plan].
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", t0)}, tracker.CategoryUnstarted, false)
		assert.False(t, card.HasPlan)
		assert.Equal(t, BoardPlanning, card.Stage)
	})

	t.Run("plan plus markers keeps both", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:plan]\nthe plan", t0),
			cmt("[human:planning-started]", t1),
		}, tracker.CategoryUnstarted, false)
		assert.True(t, card.HasPlan)
		assert.Equal(t, BoardPlanning, card.Stage)
	})
}

func TestDeriveBoardCard_HasRelatedRecord(t *testing.T) {
	t0 := time.Unix(1000, 0)

	t.Run("completed record sets the flag without shifting stage", func(t *testing.T) {
		// A [human:related] verdict is advisory content, not a stage signal — the
		// card with no other markers stays in Backlog (SC-2405).
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:related] none", t0)}, tracker.CategoryUnstarted, false)
		assert.True(t, card.HasRelatedRecord)
		assert.Equal(t, BoardBacklog, card.Stage)
	})

	t.Run("incomplete record leaves the flag unset", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:related] incomplete\ndied halfway", t0)}, tracker.CategoryUnstarted, false)
		assert.False(t, card.HasRelatedRecord)
	})

	t.Run("no related marker leaves the flag unset", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:related-started]", t0)}, tracker.CategoryUnstarted, false)
		assert.False(t, card.HasRelatedRecord)
	})
}

func TestDeriveBoardCard_shippedPartial(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)

	t.Run("marker sets the fields", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:deployed]\npr: http://x", t0),
			cmt("[human:shipped-partial]\nfollow-on: SC-3001\ndeferred: CSV export\n  cost webhook", t1),
		}, tracker.CategoryUnstarted, false)
		assert.True(t, card.ShippedPartial)
		assert.Equal(t, "SC-3001", card.ShippedPartialFollowOn)
	})

	t.Run("absent marker leaves the fields zero", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:deployed]\npr: http://x", t0),
		}, tracker.CategoryUnstarted, false)
		assert.False(t, card.ShippedPartial)
		assert.Empty(t, card.ShippedPartialFollowOn)
	})

	t.Run("latest wins", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:plan-ready]", t0),
			cmt("[human:shipped-partial]\nfollow-on: SC-3001\ndeferred: A", t1),
			cmt("[human:shipped-partial]\nfollow-on: SC-3002\ndeferred: B", t2),
		}, tracker.CategoryUnstarted, false)
		assert.Equal(t, "SC-3002", card.ShippedPartialFollowOn)
	})

	t.Run("does not move the card", func(t *testing.T) {
		// The trace decorates the card; the real stage marker (plan-ready) still
		// owns the stage — the shipped-partial marker never shifts it.
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-9", t0),
			cmt("[human:shipped-partial]\nfollow-on: SC-3001\ndeferred: A", t1),
		}, tracker.CategoryUnstarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.True(t, card.ShippedPartial)
	})
}

func TestDeriveBoardCard_Ideas(t *testing.T) {
	t0 := time.Unix(1000, 0)

	t.Run("open idea sits in Ideas regardless of markers", func(t *testing.T) {
		// The label is the single source of truth until promotion removes it —
		// even pipeline markers cannot pull an idea forward.
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:planning-started]", t0)}, tracker.CategoryUnstarted, true)
		assert.Equal(t, BoardIdeas, card.Stage)
		assert.Equal(t, BoardIdle, card.State)
	})

	t.Run("closed idea is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryClosed, true)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("done idea is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryDone, true)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("ideas rank below backlog", func(t *testing.T) {
		assert.Less(t, stageRank[BoardIdeas], stageRank[BoardBacklog])
	})
}

func TestLatestPlanComment(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("latest plan wins", func(t *testing.T) {
		body, ok := latestPlanComment([]tracker.Comment{
			cmt("[human:plan]\nold plan", t0),
			cmt("[human:plan]\nnew plan", t1),
		})
		assert.True(t, ok)
		assert.Equal(t, "new plan", body)
	})

	t.Run("header stripped, quoted header mid-body ignored", func(t *testing.T) {
		body, ok := latestPlanComment([]tracker.Comment{
			cmt("see `[human:plan]` for details", t0),
		})
		assert.False(t, ok)
		assert.Empty(t, body)
	})

	t.Run("no comments", func(t *testing.T) {
		_, ok := latestPlanComment(nil)
		assert.False(t, ok)
	})
}

func TestDeriveBoardCard_stageEnteredAt(t *testing.T) {
	planned := time.Unix(5000, 0)
	card := DeriveBoardCard([]tracker.Comment{
		cmt("[human:planning-started]", time.Unix(1000, 0)),
		cmt("[human:plan-ready]", planned),
	}, tracker.CategoryUnstarted, false)
	// The newest marker in the current stage stamps the card: for a plan-done
	// card that is when the current plan landed, which the age badge renders.
	assert.Equal(t, planned, card.StageEnteredAt)

	backlog := DeriveBoardCard(nil, tracker.CategoryUnstarted, false)
	assert.True(t, backlog.StageEnteredAt.IsZero(), "a no-marker backlog card carries no stage time")
}

// A running stage's deciding marker stamped with a daemon id must carry that
// id onto the derived card, so the stuck-running reconcile pass can tell a
// peer daemon's live card from its own (SC-1450).
func TestDeriveBoardCard_stampsStageDaemonID(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(marker.Sign(ImplementationStartedHeader, "d1", ""), time.Unix(1000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, "d1", card.StageDaemonID)
}

// An unstamped deciding marker (today's single-daemon boards) must leave
// StageDaemonID empty, keeping the stuck-running pass's local grace path
// unchanged (SC-1450).
func TestDeriveBoardCard_unstampedStageDaemonEmpty(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Empty(t, card.StageDaemonID)
}

// SC-1320 regression: after a decision is recorded ([human:option-chosen]) and
// the relaunch is deferred (launch gate blockers → no fresh started marker), the
// card must read as queued for the chosen stage — never collapse to the stale
// pre-decision running marker (which the stuck-running pass would then red).
func TestDeriveBoardCard_OptionChosenQueuesChosenStage(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		cmt(PlanningStartedHeader, base),
		cmt("[human:options]\nstage: planning\ncontext: two directions\n1: A\n2: B", base.Add(1*time.Minute)),
		cmt(OptionChosenHeader+" 2: B", base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardQueued, card.State, "a recorded decision with no later started marker must read as queued")
	assert.Empty(t, card.Options, "the chosen block is consumed")
	assert.Empty(t, card.Error, "a queued card is not a failure")
}

// SC-1320 regression: the fresh started marker supersedes the queued state
// (latest-wins) — once ApplyOption's relaunch posts planning-started the card
// reads running again, exactly as before.
func TestDeriveBoardCard_StartedMarkerSupersedesQueued(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		cmt("[human:options]\nstage: planning\n1: A\n2: B", base),
		cmt(OptionChosenHeader+" 2: B", base.Add(1*time.Minute)),
		cmt(PlanningStartedHeader, base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
}

// A card whose newest done-stage marker is a pr-review-started marker is
// mid-loop: it must derive done/running with DeployPhase "pr-review" so the
// board badges it "PR review…" rather than "deploying…".
func TestDeriveBoardCard_DeployPhasePRReview(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Equal(t, "pr-review", card.DeployPhase)
}

// doneStageLoopActive answers the COARSER question — is the review→fix loop
// mid-flight at all — for the re-drive pass (board_reconcile.go:262) and the
// stuck-running guard (:370). Splitting the phase out must not narrow it: both
// halves keep the loop card away from those passes.
func TestDoneStageLoopActive_MatchesBothLoopHalves(t *testing.T) {
	review := []tracker.Comment{cmt(PRReviewStartedHeader, time.Unix(2, 0))}
	fix := []tracker.Comment{cmt(PRFixStartedHeader, time.Unix(2, 0))}
	deploy := []tracker.Comment{cmt(DeployedHeader, time.Unix(2, 0))}

	assert.True(t, doneStageLoopActive(review))
	assert.True(t, doneStageLoopActive(fix))
	assert.False(t, doneStageLoopActive(deploy))
}

// An implementation stage that reported the substrate was down derives to the
// distinct BoardOutage state (not BoardFailed) and carries the reason line so
// the badge can read WHAT is down (SC-2307).
func TestDeriveBoardCard_OutageState(t *testing.T) {
	base := time.Unix(1, 0)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt(ImplementationOutageHeader+"\nop timed out", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardOutage, card.State)
	assert.NotEqual(t, BoardFailed, card.State)
	assert.Equal(t, "op timed out", card.Error)
}

// An outage marker is transient: a strictly-newer *-started marker from the
// reconcile relaunch retires it and the card follows the current activity
// (SC-2307), exactly like a stale failure does.
func TestDeriveBoardCard_OutageSupersededByNewerStarted(t *testing.T) {
	base := time.Unix(1, 0)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt(ImplementationOutageHeader+"\nop timed out", base.Add(time.Minute)),
		cmt(ImplementationStartedHeader, base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardRunning, card.State, "a newer started marker retires the outage")
}

// A plain deploy (deploy-started, not a loop marker) has no loop sub-phase, so
// DeployPhase stays empty and the badge reads "deploying…".
func TestDeriveBoardCard_DeployPhaseEmptyForPlainDeploy(t *testing.T) {
	comments := []tracker.Comment{cmt(DeployStartedHeader, time.Unix(2, 0))}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Empty(t, card.DeployPhase)
}

// A chosen rebuild posts a strictly-newer implementation-started marker; the
// generalized supersession must let it retire the loop marker so the card
// leaves the done lane back to Building.
func TestDeriveBoardCard_RebuildSupersedesLoopMarker(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRFixStartedHeader, time.Unix(2, 0)),
		cmt(ImplementationStartedHeader, time.Unix(3, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Empty(t, card.DeployPhase)
}

// SC-1957: a question raised late in a card's life deliberately names an
// EARLIER rework stage — answering it means going back and redoing that
// work. That is still a deliberate human pause, not a hang, so the card must
// derive BoardIdle with the question attached rather than staying
// BoardRunning (which is exactly what let recovery erase it before).
func TestDeriveBoardCard_OpenEarlierStageOptionsIsWaiting(t *testing.T) {
	base := time.Unix(1, 0)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt("[human:options]\nstage: planning\ncontext: rework?\n1: a\n2: b\n3: c", base.Add(time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.Stage)
	assert.Equal(t, BoardIdle, card.State, "an earlier-stage rework question must pause, not keep running")
	assert.Len(t, card.Options, 3)
	assert.Equal(t, BoardPlanning, card.OptionsStage)
}

// SC-1957 residual safety net: even if a card was already reddened by a
// *-failed marker before the fix (or by some other future path), an open
// at-or-before options block newer than that failure must still surface as
// waiting-on-human rather than a plain failure — the third acceptance
// criterion, "where a question is erased anyway, that should be visible".
func TestDeriveBoardCard_FailedWithOpenOptionsSurfacesAsWaiting(t *testing.T) {
	base := time.Unix(1, 0)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, base),
		cmt("[human:options]\nstage: planning\ncontext: rework?\n1: a\n2: b", base.Add(time.Minute)),
		cmt("[human:implementation-failed]\nstale hang", base.Add(2*time.Minute)),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardIdle, card.State, "an open question surviving a stale failure must surface, not stay red")
	assert.Empty(t, card.Error)
	assert.Len(t, card.Options, 2)
}

// SC-2699: a pre-planning gate's superseded verdict carries onto the card as a
// stop decision, with the linked parent key and the recorded reasoning — so a
// decided card is distinguishable from one merely waiting in Backlog.
func TestDeriveBoardCard_ticketReviewStop_superseded(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(TicketReviewStartedHeader, t0),
		cmt(TicketReviewedHeader+" superseded\nroot: same as ticket\nlinked: SC-100\n\nSame surface as SC-100, which carries the work", t1),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardBacklog, card.Stage)
	assert.Equal(t, BoardDone, card.State)
	assert.Equal(t, "superseded", card.StopDecision)
	assert.Equal(t, "SC-100", card.StopLinkedKey)
	assert.Contains(t, card.StopReasoning, "carries the work")
}

// SC-2699: an escalated verdict names the design ticket the gate created to
// unblock the work; the card carries that key and the evidence.
func TestDeriveBoardCard_ticketReviewStop_escalated(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(TicketReviewStartedHeader, t0),
		cmt(TicketReviewedHeader+" escalated\nroot: same as ticket\nlinked: SC-200\n\nNeeds the auth model decided first", t1),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, "escalated", card.StopDecision)
	assert.Equal(t, "SC-200", card.StopLinkedKey)
	assert.Contains(t, card.StopReasoning, "auth model")
}

// SC-2699: a rejected verdict names no ticket — the linked key is empty, and
// the card still carries the decision and the evidence that makes it a
// non-problem.
func TestDeriveBoardCard_ticketReviewStop_rejectedNoLink(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(TicketReviewStartedHeader, t0),
		cmt(TicketReviewedHeader+" rejected\nroot: same as ticket\n\nWorks as designed; not a bug", t1),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, "rejected", card.StopDecision)
	assert.Empty(t, card.StopLinkedKey)
	assert.Contains(t, card.StopReasoning, "not a bug")
}

// SC-2699: a re-dispatch posts a later planning-started marker, which becomes
// the deciding marker (furthest-stage/latest-wins). The stale stop decision
// must clear — a re-dispatched card carries no decision, so the judgement is
// never silently re-paid without the human having seen it first.
func TestDeriveBoardCard_ticketReviewStop_clearedByReDispatch(t *testing.T) {
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)
	comments := []tracker.Comment{
		cmt(TicketReviewedHeader+" superseded\nlinked: SC-100\n\nduplicate", t1),
		cmt(PlanningStartedHeader, t2),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Empty(t, card.StopDecision, "supersession clears the stale stop decision")
	assert.Empty(t, card.StopLinkedKey)
}

// SC-2699: a `ready` verdict is not a stop head — it continues into planning,
// so no stop fields are set and the card advances as before.
func TestDeriveBoardCard_ticketReviewReady_noStop(t *testing.T) {
	t1 := time.Unix(2000, 0)
	comments := []tracker.Comment{
		cmt(TicketReviewedHeader+" ready\nroot: same as ticket", t1),
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Empty(t, card.StopDecision, "ready is not a stop head")
	assert.Empty(t, card.StopLinkedKey)
	assert.Empty(t, card.StopReasoning)
}

// SC-2699: an undecided Backlog card (no markers) carries no stop fields — it
// renders exactly as it does today.
func TestDeriveBoardCard_noMarkers_noStop(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardBacklog, card.Stage)
	assert.Empty(t, card.StopDecision)
	assert.Empty(t, card.StopLinkedKey)
	assert.Empty(t, card.StopReasoning)
}

// SC-3555: a terminal determination about the WHOLE ticket must retire the
// phantom run it supersedes, whatever stage that run belonged to. The planner
// posts [human:nothing-to-do] after earlier implementation runs died without a
// terminal marker; furthest-stage-wins otherwise pins the card on the phantom
// and it renders "fixing…" forever with no agent behind it.
func TestDeriveBoardCard_nothingToDoOverPhantomRun(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("under a stale implementation run", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(ImplementationStartedHeader, t0),
			cmt(NothingToDoHeader+"\nevidence: already merged in PR #271", t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardResolved, card.State)
		assert.Equal(t, t1, card.StageEnteredAt, "the terminal is the deciding marker")
	})

	t.Run("under a stale deploy run", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(DeployStartedHeader, t0),
			cmt(NothingToDoHeader+"\nevidence: already merged in PR #271", t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardResolved, card.State)
	})
}

// SC-3555: the implementation stage's clean terminal has the same shape one
// column over — a no-fix-needed verdict under a phantom verification or deploy
// run must read as the resolved determination, not as a running build.
func TestDeriveBoardCard_noFixNeededOverPhantomRun(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("under a stale review run", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(ReviewStartedHeader, t0),
			cmt(NoFixNeededHeader+"\ntriage: not-a-bug", t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardResolved, card.State)
	})

	t.Run("under a stale deploy run", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(DeployStartedHeader, t0),
			cmt(NoFixNeededHeader+"\ntriage: undetermined", t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardResolved, card.State)
	})
}

// SC-3555: a pre-planning gate's stop verdict (backlog/done, the lowest rank of
// all) loses to any pipeline phantom — and with it the card's whole stop
// decision, because ticketReviewStop reads it off the deciding marker. Feeding
// the terminal in as `latest` restores stage, state AND the recorded reason.
func TestDeriveBoardCard_ticketReviewStopOverPhantomImplementation(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	card := DeriveBoardCard([]tracker.Comment{
		cmt(ImplementationStartedHeader, t0),
		cmt(TicketReviewedHeader+" rejected\nroot: same as ticket\n\nWorks as designed; not a bug", t1),
	}, tracker.CategoryStarted, false)

	assert.Equal(t, BoardBacklog, card.Stage)
	assert.Equal(t, BoardDone, card.State)
	assert.Equal(t, "rejected", card.StopDecision)
	assert.Contains(t, card.StopReasoning, "not a bug")
}

// SC-3555: the override is newest-marker-only on purpose — a genuine
// re-dispatch after a terminal must move the card onto the new run rather than
// leaving it parked on a retired determination.
func TestDeriveBoardCard_terminalRetiredByReDispatch(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)

	t.Run("a later planning-started wins", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(NothingToDoHeader+"\nevidence: already merged", t0),
			cmt(PlanningStartedHeader, t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("a later implementation-started wins", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt(NothingToDoHeader+"\nevidence: already merged", t0),
			cmt(ImplementationStartedHeader, t1),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("re-buried terminal falls back to furthest-stage-wins", func(t *testing.T) {
		// Deliberately preserved: once a marker lands after the terminal, the
		// override stands down and the ordinary rules decide. The card must not
		// keep advertising the retired determination — supersededByNewerMarker is
		// NOT widened to demote a running marker (SC-910/SC-1320/SC-1669).
		card := DeriveBoardCard([]tracker.Comment{
			cmt(ImplementationStartedHeader, t0),
			cmt(NothingToDoHeader+"\nevidence: already merged", t1),
			cmt(PlanningStartedHeader, t2),
		}, tracker.CategoryStarted, false)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
		assert.NotEqual(t, BoardResolved, card.State)
	})
}

// SC-3555: terminality is registered data, so the next cross-stage terminal
// cannot re-open the hole by forgetting a bespoke override. Progress markers and
// non-stop gate verdicts must stay out of the set.
func TestIsTerminalResolution(t *testing.T) {
	t.Run("registered terminals", func(t *testing.T) {
		assert.True(t, isTerminalResolution(NothingToDoHeader+"\nevidence: merged"))
		assert.True(t, isTerminalResolution(NoFixNeededHeader))
		assert.True(t, isTerminalResolution(NeedsPlanningHeader+"\n"+needsPlanningReason))
	})
	t.Run("ticket-review stop heads", func(t *testing.T) {
		assert.True(t, isTerminalResolution(TicketReviewedHeader+" rejected\n\nworks as designed"))
		assert.True(t, isTerminalResolution(TicketReviewedHeader+" superseded\nlinked: SC-100"))
		assert.True(t, isTerminalResolution(TicketReviewedHeader+" escalated\nlinked: SC-200"))
	})
	t.Run("continuing verdicts and progress markers are not terminals", func(t *testing.T) {
		assert.False(t, isTerminalResolution(TicketReviewedHeader+" ready"))
		assert.False(t, isTerminalResolution(TicketReviewStartedHeader))
		assert.False(t, isTerminalResolution(PlanningStartedHeader))
		assert.False(t, isTerminalResolution(ImplementationStartedHeader))
		assert.False(t, isTerminalResolution(PlanReadyHeader+"\nengineering: HUM-7"))
		assert.False(t, isTerminalResolution("just a human comment about nothing-to-do"))
	})
	t.Run("a signed marker body still classifies", func(t *testing.T) {
		assert.True(t, isTerminalResolution(NothingToDoHeader+"\nmachine: 4f3add9a\nbuild: abc123\n\nevidence: merged"))
	})
}

// Both launches of the PR review→fix loop post a marker, and so does its
// escalation — success was the only outcome that recorded nothing. A reader, or
// a daemon that restarted, could not tell an approved review whose merge is
// running from a review still in flight. The passing marker also retires the
// loop sub-phase, so the badge stops claiming a review is in progress for the
// whole of the CI gate, rebase and merge.
func TestDeriveBoardCard_APassingLoopRetiresTheReviewPhase(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	loop := []tracker.Comment{
		{Body: DeployStartedHeader, Created: base},
		{Body: PRReviewStartedHeader, Created: base.Add(time.Minute)},
	}
	mid := DeriveBoardCard(loop, tracker.CategoryUnstarted, false)
	assert.Equal(t, "pr-review", mid.DeployPhase, "while the loop runs the card names the sub-phase")

	passed := append(loop, tracker.Comment{Body: PRReviewPassedHeader, Created: base.Add(2 * time.Minute)})
	card := DeriveBoardCard(passed, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State, "the merge is still work in progress")
	assert.Empty(t, card.DeployPhase, "the review is over — the card must stop saying 'PR review…'")
}

// SC-3615: the deploy fixer's escalation told the reader to "check the PR and
// its CI, then re-run Deploy" and named nothing. Re-running changes nothing
// about the branch, so the same check fails identically — the card's one offered
// move reproduced its own failure. The cause was on the ticket the whole time:
// the gate writes the failing checks onto the deploy-fix-started marker when it
// dispatches the fixer.
func TestDeployFixEscalation_NamesTheFailureAndRefusesAPointlessRetry(t *testing.T) {
	dispatched := "CI checks failed on the pull request (failing: frontend-test) — fix the failing checks, then re-run Deploy"

	reason := deployFixEscalationReason(ExitRetryable, dispatched)
	assert.Contains(t, reason, "frontend-test", "the blocking check must be named on the card")
	assert.Contains(t, reason, "will hit the same failure", "a retry that cannot work must not be the advice")

	// With nothing recorded there is nothing to name, and the old wording stands
	// rather than inventing a cause.
	assert.Equal(t,
		"the deploy fixer stopped without recovering the deploy — check the PR and its CI, then re-run Deploy",
		deployFixEscalationReason(ExitRetryable, ""))
}

// The headline is recovered from the newest dispatch, so a second round names
// what the second dispatch was sent to fix rather than the first.
func TestDispatchedFailure_TakesTheNewestDispatch(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	comments := []tracker.Comment{
		{Body: DeployFixStartedHeader + "\nthe first failure\npr: u\nnumber: 1\nbranch: b", Created: base},
		{Body: DeployFixStartedHeader + "\nthe second failure\npr: u\nnumber: 1\nbranch: b", Created: base.Add(time.Minute)},
	}
	assert.Equal(t, "the second failure", dispatchedFailure(comments))
	assert.Empty(t, dispatchedFailure(nil), "no dispatch recorded means nothing to quote")
}

// TestDeriveBoardCard_DeployPhasePRFix covers SC-4151 F15 and the observed
// SC-3322/SC-3569 case: the loop's two halves are separate agents in separate
// containers everywhere else, and the badge called both of them the review —
// so a card whose live container was board-SC-3322-prfix running human-pr-fixer
// read "PR review…" for the whole loop, sending a reader to the wrong log.
func TestDeriveBoardCard_DeployPhasePRFix(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Equal(t, DeployPhasePRFix, card.DeployPhase)
}

// The loop swinging back to a second review round names the review again.
func TestDeriveBoardCard_DeployPhaseFollowsTheNewestHalf(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(4, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, DeployPhasePRReview, card.DeployPhase)
}

// TestDeployPhaseFor_FailedLoopCard covers SC-4151 A1's derivation half: a red
// done-stage card names the loop half that was started under the failure, so
// AgentNamesForCard can ask whether it is still running. The badge never reads
// this — it consults DeployPhase only while running.
func TestDeployPhaseFor_FailedLoopCard(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(markerBody(failureMarker(MarkerPRReviewFailed, "the PR reviewer stopped before recording a verdict")), time.Unix(3, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardFailed, card.State)
	assert.Equal(t, DeployPhasePRReview, card.DeployPhase, "the half that was running under the red")
}

// The fix half reddened is named as the fix half.
func TestDeployPhaseFor_FailedFixHalf(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
		cmt(markerBody(failureMarker(MarkerPRReviewFailed, "the PR fixer stopped before recording an exit")), time.Unix(4, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, DeployPhasePRFix, card.DeployPhase)
}

// A deploy-path failure names no loop half: answering it with a PR-loop
// container left over from an earlier round would be a false "still working".
func TestDeployPhaseFor_FailedDeployNamesNoHalf(t *testing.T) {
	comments := []tracker.Comment{
		cmt(prReviewStartedBody("https://example/pr/7", 7, "feat/x"), time.Unix(2, 0)),
		cmt(PRFixStartedHeader, time.Unix(3, 0)),
		cmt(markerBody(marker.Marker{Type: MarkerDeployFixStarted, Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x"), Body: "CI failed"}, "pr", "number", "branch"), time.Unix(4, 0)),
		cmt(markerBody(failureMarker(MarkerDeployFailed, "the deploy fixer could not resolve the conflict")), time.Unix(5, 0)),
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardFailed, card.State)
	assert.Empty(t, card.DeployPhase, "the deploy path started last, so no loop half is claimed")
}

// SC-4406 replays the SC-3853 sequence: the implementation stage is relaunched,
// and twenty minutes later a done-stage PR-review failure lands and takes the
// card. The placement is right — the failure IS the newest marker — but the
// implementation run it painted over is still going, so the card must carry the
// stage that is working for the viewer to join a container against.
func TestDeriveBoardCard_RunningStageNamesTheOtherLiveStage(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
		cmt(PRReviewStartedHeader, time.Unix(1100, 0)),
		cmt(PRReviewFailedHeader+"\nthe PR reviewer stopped before recording a verdict", time.Unix(2000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardFailed, card.State)
	assert.Equal(t, BoardImplementation, card.RunningStage)
}

// A stage that finished is not a stage that is running: its own newest marker is
// terminal, so it is never offered as the ticket's live run.
func TestDeriveBoardCard_RunningStageIgnoresAFinishedStage(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
		cmt(ReadyForReviewHeader+"\nbranch: feat/x", time.Unix(1100, 0)),
		cmt(PRReviewFailedHeader+"\nno verdict", time.Unix(2000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardFailed, card.State)
	assert.Empty(t, card.RunningStage)
}

// Only a FAILED card carries the field. A running or done card's liveness
// question is about its own stage, and answering it with a neighbouring stage's
// container would make a dead run read as live.
func TestDeriveBoardCard_RunningStageIsFailedCardsOnly(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
		cmt(ReviewStartedHeader, time.Unix(2000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardVerification, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
	assert.Empty(t, card.RunningStage)
}

// With two other stages showing a start, the newest wins — the same
// latest-marker rule the placement itself follows, so the card never names an
// older run over a newer one.
func TestDeriveBoardCard_RunningStagePicksTheNewestStart(t *testing.T) {
	card := DeriveBoardCard([]tracker.Comment{
		cmt(PlanningStartedHeader, time.Unix(1000, 0)),
		cmt(ImplementationStartedHeader, time.Unix(1500, 0)),
		cmt(PRReviewFailedHeader+"\nno verdict", time.Unix(2000, 0)),
	}, tracker.CategoryUnstarted, false)

	assert.Equal(t, BoardImplementation, card.RunningStage)
}
