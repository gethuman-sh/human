package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/proxy"
)

func TestModelOutcomeSink_RecordAndQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewModelOutcomeSink(ctx)

	s.Record(proxy.ModelCallOutcome{Ticket: "SC-1", Stage: "implementation", Class: proxy.ClassOK, StatusCode: 200})
	s.Record(proxy.ModelCallOutcome{Ticket: "SC-1", Stage: "implementation", Class: proxy.ClassAuth, StatusCode: 401})

	require.Eventually(t, func() bool {
		c, ok := s.LatestClass("SC-1", "implementation")
		return ok && c == proxy.ClassAuth
	}, time.Second, 10*time.Millisecond, "latest class reflects the most recent outcome")

	outcomes := s.Outcomes()
	assert.Len(t, outcomes, 2)

	_, ok := s.LatestClass("SC-9", "planning")
	assert.False(t, ok, "an unseen key has no latest class")
}

func TestModelOutcomeSink_NilSafe(t *testing.T) {
	var s *ModelOutcomeSink
	assert.NotPanics(t, func() { s.Record(proxy.ModelCallOutcome{}) })
	assert.Nil(t, s.Outcomes())
	assert.Zero(t, s.Dropped())
	_, ok := s.LatestClass("SC-1", "implementation")
	assert.False(t, ok)
}

func TestModelOutcomeSink_DropsWhenFull(t *testing.T) {
	// A sink whose drain goroutine never runs: the channel fills and further
	// records drop rather than block (constraint: recording must not slow a call).
	s := &ModelOutcomeSink{
		ch:          make(chan proxy.ModelCallOutcome, 2),
		byKey:       map[outcomeKey][]proxy.ModelCallOutcome{},
		latestClass: map[outcomeKey]string{},
	}
	for i := 0; i < 5; i++ {
		s.Record(proxy.ModelCallOutcome{Ticket: "SC-1"})
	}
	assert.Equal(t, int64(3), s.Dropped(), "3 of 5 dropped once the cap-2 channel filled")
}

func TestModelOutcomeSink_BoundsPerKeyHistory(t *testing.T) {
	s := &ModelOutcomeSink{
		byKey:       map[outcomeKey][]proxy.ModelCallOutcome{},
		latestClass: map[outcomeKey]string{},
	}
	for i := 0; i < maxOutcomesPerKey+50; i++ {
		s.store(proxy.ModelCallOutcome{Ticket: "SC-1", Stage: "impl", StatusCode: i})
	}
	got := s.Outcomes()
	require.Len(t, got, maxOutcomesPerKey, "per-key history is bounded to the newest N")
	// The oldest 50 were evicted; the first retained call is index 50.
	assert.Equal(t, 50, got[0].StatusCode)
	assert.Equal(t, maxOutcomesPerKey+49, got[len(got)-1].StatusCode)
}

func TestModelOutcomeSink_UnattributedGroupsSeparately(t *testing.T) {
	s := &ModelOutcomeSink{
		byKey:       map[outcomeKey][]proxy.ModelCallOutcome{},
		latestClass: map[outcomeKey]string{},
	}
	s.store(proxy.ModelCallOutcome{Class: proxy.ClassNetwork})
	c, ok := s.LatestClass("", "")
	assert.True(t, ok)
	assert.Equal(t, proxy.ClassNetwork, c)
}
