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
		card := DeriveBoardCard(nil, tracker.CategoryUnstarted)
		assert.Equal(t, BoardBacklog, card.Stage)
		assert.Equal(t, BoardIdle, card.State)
	})

	t.Run("no markers closed is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryClosed)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("no markers done is hidden", func(t *testing.T) {
		card := DeriveBoardCard(nil, tracker.CategoryDone)
		assert.Equal(t, BoardHidden, card.Stage)
	})

	t.Run("planning-started is planning running", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:planning-started]", t0)}, tracker.CategoryUnstarted)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
	})

	t.Run("plan-ready with eng key", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-7", t0)}, tracker.CategoryUnstarted)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "HUM-7", card.EngineeringKey)
	})

	t.Run("furthest stage wins", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-7", t0),
			cmt("[human:implementation-started]", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardRunning, card.State)
		assert.Equal(t, "HUM-7", card.EngineeringKey)
	})

	t.Run("latest within stage supersedes", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:planning-started]", t0),
			cmt("[human:plan-ready]\nengineering: HUM-9", t1),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted)
		assert.Equal(t, BoardPlanning, card.Stage)
		assert.Equal(t, BoardDone, card.State)
	})

	t.Run("ready-for-review carries branch and eng", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x\ncommits: abc", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "feat/x", card.Branch)
		assert.Equal(t, "HUM-9", card.EngineeringKey)
	})

	t.Run("implementation-failed records error", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:implementation-failed]\ncompile error in foo.go", t0),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted)
		assert.Equal(t, BoardImplementation, card.Stage)
		assert.Equal(t, BoardFailed, card.State)
		assert.Equal(t, "[human:implementation-failed]", card.Error)
	})

	t.Run("full chain ending pr-pushed", func(t *testing.T) {
		comments := []tracker.Comment{
			cmt("[human:plan-ready]\nengineering: HUM-9", t0),
			cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", t1),
			cmt("[human:review-complete]", t2),
			cmt("[human:pr-pushed]\npr: https://example/pr/1", t2.Add(time.Second)),
		}
		card := DeriveBoardCard(comments, tracker.CategoryUnstarted)
		assert.Equal(t, BoardDoneStage, card.Stage)
		assert.Equal(t, BoardDone, card.State)
		assert.Equal(t, "https://example/pr/1", card.PRURL)
		assert.Equal(t, "feat/x", card.Branch)
		assert.Equal(t, "HUM-9", card.EngineeringKey)
	})
}

func TestDeriveBoardCard_HasPlan(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	t.Run("plan comment sets HasPlan without shifting stage", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan]\n\n## Steps\n1. do it", t0)}, tracker.CategoryUnstarted)
		assert.True(t, card.HasPlan)
		// The plan is content, not a stage signal — the card stays in Backlog.
		assert.Equal(t, BoardBacklog, card.Stage)
	})

	t.Run("plan-ready is not a plan comment", func(t *testing.T) {
		// Prefix isolation: [human:plan-ready] must not read as [human:plan].
		card := DeriveBoardCard([]tracker.Comment{cmt("[human:plan-ready]\nengineering: HUM-9", t0)}, tracker.CategoryUnstarted)
		assert.False(t, card.HasPlan)
		assert.Equal(t, BoardPlanning, card.Stage)
	})

	t.Run("plan plus markers keeps both", func(t *testing.T) {
		card := DeriveBoardCard([]tracker.Comment{
			cmt("[human:plan]\nthe plan", t0),
			cmt("[human:planning-started]", t1),
		}, tracker.CategoryUnstarted)
		assert.True(t, card.HasPlan)
		assert.Equal(t, BoardPlanning, card.Stage)
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
