package cmdplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

func c(body string, sec int64) tracker.Comment {
	return tracker.Comment{Body: body, Created: time.Unix(sec, 0)}
}

func TestExtractPlan(t *testing.T) {
	t.Run("latest plan wins, header stripped", func(t *testing.T) {
		body, ok := ExtractPlan([]tracker.Comment{
			c("[human:plan]\n\n## Old", 1),
			c("[human:plan]\n\n## New\n```go\nx := 1\n```", 2),
		})
		assert.True(t, ok)
		assert.Equal(t, "## New\n```go\nx := 1\n```", body)
	})

	t.Run("plan-ready is not a plan", func(t *testing.T) {
		_, ok := ExtractPlan([]tracker.Comment{c("[human:plan-ready]\nengineering: HUM-9", 1)})
		assert.False(t, ok)
	})

	t.Run("quoted header mid-body is not a plan", func(t *testing.T) {
		_, ok := ExtractPlan([]tracker.Comment{c("see `[human:plan]` for details", 1)})
		assert.False(t, ok)
	})

	t.Run("no comments", func(t *testing.T) {
		_, ok := ExtractPlan(nil)
		assert.False(t, ok)
	})
}
