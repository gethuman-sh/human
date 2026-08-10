package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/proxy"
)

// fakeLedger records the calls persisted through the sink's write seam.
type fakeLedger struct {
	mu    sync.Mutex
	calls []costledger.CallRecord
}

func (f *fakeLedger) InsertCall(_ context.Context, r costledger.CallRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	return nil
}

func (f *fakeLedger) recorded() []costledger.CallRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]costledger.CallRecord(nil), f.calls...)
}

func TestSink_PersistsAttributed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	led := &fakeLedger{}
	s := NewModelOutcomeSink(ctx).WithLedger(led, func(ticket string) string { return "proj-" + ticket }, zerolog.Nop())

	s.Record(proxy.ModelCallOutcome{
		Ticket: "SC-1", Stage: "implementation", Model: "claude-opus-4-8",
		InputTokens: 100, OutputTokens: 200, CacheCreateTokens: 50, CacheReadTokens: 900,
		Duration: 1500 * time.Millisecond, Class: proxy.ClassOK, StatusCode: 200,
	})

	require.Eventually(t, func() bool { return len(led.recorded()) == 1 }, time.Second, 10*time.Millisecond)
	got := led.recorded()[0]
	assert.Equal(t, "proj-SC-1", got.Project, "the resolver's project is stamped on the row")
	assert.Equal(t, "SC-1", got.Ticket)
	assert.Equal(t, "implementation", got.Stage)
	assert.Equal(t, "claude-opus-4-8", got.Model)
	assert.Equal(t, 100, got.InputTokens)
	assert.Equal(t, 200, got.OutputTokens)
	assert.Equal(t, 50, got.CacheCreateTokens)
	assert.Equal(t, 900, got.CacheReadTokens)
	assert.Equal(t, int64(1500), got.DurationMs)
}

func TestSink_UnattributedNotPersisted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	led := &fakeLedger{}
	s := NewModelOutcomeSink(ctx).WithLedger(led, nil, zerolog.Nop())

	s.Record(proxy.ModelCallOutcome{Ticket: "", Stage: "", Class: proxy.ClassNetwork})

	// The outcome still lands in-memory for LatestClass under the zero key.
	require.Eventually(t, func() bool {
		c, ok := s.LatestClass("", "")
		return ok && c == proxy.ClassNetwork
	}, time.Second, 10*time.Millisecond)
	assert.Empty(t, led.recorded(), "an unattributed outcome is not persisted — it cannot be tied to a ticket")
}

func TestSink_WithLedgerNilSafe(t *testing.T) {
	var s *ModelOutcomeSink
	assert.NotPanics(t, func() { s.WithLedger(&fakeLedger{}, nil, zerolog.Nop()) })
}

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
		ch:    make(chan proxy.ModelCallOutcome, 2),
		byKey: map[outcomeKey][]proxy.ModelCallOutcome{},
	}
	for i := 0; i < 5; i++ {
		s.Record(proxy.ModelCallOutcome{Ticket: "SC-1"})
	}
	assert.Equal(t, int64(3), s.Dropped(), "3 of 5 dropped once the cap-2 channel filled")
}

func TestModelOutcomeSink_BoundsPerKeyHistory(t *testing.T) {
	s := &ModelOutcomeSink{
		byKey: map[outcomeKey][]proxy.ModelCallOutcome{},
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

// LatestClass is derived from the history rather than kept beside it, so the
// eviction that bounds the history must not be able to change what it answers:
// the bound drops the OLDEST, and the newest outcome is the one it reports.
func TestModelOutcomeSink_LatestClassSurvivesEviction(t *testing.T) {
	s := &ModelOutcomeSink{
		byKey: map[outcomeKey][]proxy.ModelCallOutcome{},
	}
	for i := 0; i < maxOutcomesPerKey+50; i++ {
		s.store(proxy.ModelCallOutcome{Ticket: "SC-1", Stage: "impl", Class: proxy.ClassOK})
	}
	s.store(proxy.ModelCallOutcome{Ticket: "SC-1", Stage: "impl", Class: proxy.ClassRateLimit})

	c, ok := s.LatestClass("SC-1", "impl")
	assert.True(t, ok)
	assert.Equal(t, proxy.ClassRateLimit, c, "the newest outcome's class, not one an eviction left behind")
}

func TestModelOutcomeSink_UnattributedGroupsSeparately(t *testing.T) {
	s := &ModelOutcomeSink{
		byKey: map[outcomeKey][]proxy.ModelCallOutcome{},
	}
	s.store(proxy.ModelCallOutcome{Class: proxy.ClassNetwork})
	c, ok := s.LatestClass("", "")
	assert.True(t, ok)
	assert.Equal(t, proxy.ClassNetwork, c)
}
