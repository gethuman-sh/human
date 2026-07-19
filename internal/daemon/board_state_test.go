package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

func cmt(body string, t time.Time) tracker.Comment {
	return tracker.Comment{Body: body, Created: t}
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
