package daemon

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/proxy"
)

// costLedger is the durable write seam the sink persists attributed outcomes
// through; internal/costledger.Store satisfies it. Kept as an interface so this
// package needs no concrete ledger at test time.
type costLedger interface {
	InsertCall(ctx context.Context, r costledger.CallRecord) error
}

// modelOutcomeChanCap bounds the buffered hand-off channel between the request
// path and the drain goroutine. A full channel drops the measurement rather
// than blocking, so accounting can never back-pressure a model call (SC-2555
// constraint: recording must not slow a call down).
const modelOutcomeChanCap = 1024

// maxOutcomesPerKey bounds the retained history per (ticket, stage). This is a
// display/analysis buffer, not an audit log: a small per-key cap keeps memory
// flat under a hostile burst while still holding enough recent calls to read a
// stage's behaviour.
const maxOutcomesPerKey = 200

// outcomeKey is the attribution a run's outcomes are grouped under. An
// unattributed outcome (empty ticket and stage) groups under the zero key,
// which is deliberately kept rather than dropped.
type outcomeKey struct {
	ticket string
	stage  string
}

// ModelOutcomeSink accepts content-free model-call outcomes on a buffered
// channel and drains them into a bounded in-memory store from a single
// goroutine — the analysis-off-the-request-path half of the SC-2555 boundary
// recorder. Record is non-blocking; a full channel increments a dropped counter
// and returns. A nil *ModelOutcomeSink is a valid no-op so the proxy can be
// wired without one.
type ModelOutcomeSink struct {
	ch      chan proxy.ModelCallOutcome
	dropped atomic.Int64

	mu          sync.Mutex
	byKey       map[outcomeKey][]proxy.ModelCallOutcome
	latestClass map[outcomeKey]string

	// ledger persists attributed outcomes durably per ticket; nil keeps the sink
	// memory-only. resolveProject maps a ticket key to its project identity so two
	// projects' colliding keys never merge.
	ledger         costLedger
	resolveProject func(ticket string) string
	logger         zerolog.Logger
}

// WithLedger attaches durable per-ticket persistence. resolveProject may be nil
// (project then defaults to ""). Safe on a nil sink.
func (s *ModelOutcomeSink) WithLedger(ledger costLedger, resolveProject func(ticket string) string, logger zerolog.Logger) *ModelOutcomeSink {
	if s == nil {
		return s
	}
	s.ledger = ledger
	s.resolveProject = resolveProject
	s.logger = logger
	return s
}

// NewModelOutcomeSink creates a sink and starts its drain goroutine, which runs
// until ctx is cancelled.
func NewModelOutcomeSink(ctx context.Context) *ModelOutcomeSink {
	s := &ModelOutcomeSink{
		ch:          make(chan proxy.ModelCallOutcome, modelOutcomeChanCap),
		byKey:       make(map[outcomeKey][]proxy.ModelCallOutcome),
		latestClass: make(map[outcomeKey]string),
	}
	go s.run(ctx)
	return s
}

// Record enqueues an outcome for async storage. It never blocks: a full channel
// drops the measurement and increments the dropped counter. Safe on a nil sink.
func (s *ModelOutcomeSink) Record(o proxy.ModelCallOutcome) {
	if s == nil {
		return
	}
	select {
	case s.ch <- o:
	default:
		s.dropped.Add(1)
	}
}

// run drains the channel into the store until ctx is cancelled.
func (s *ModelOutcomeSink) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-s.ch:
			s.store(o)
		}
	}
}

// store appends an outcome to its per-key history (bounded to the newest
// maxOutcomesPerKey) and records the key's latest class.
func (s *ModelOutcomeSink) store(o proxy.ModelCallOutcome) {
	k := outcomeKey{ticket: o.Ticket, stage: o.Stage}
	s.mu.Lock()
	hist := append(s.byKey[k], o)
	if len(hist) > maxOutcomesPerKey {
		// Drop the oldest so the newest calls — the ones a reader wants — survive.
		hist = hist[len(hist)-maxOutcomesPerKey:]
	}
	s.byKey[k] = hist
	s.latestClass[k] = o.Class
	s.mu.Unlock()

	// Persist attributed outcomes durably (SC-2847). An unattributed outcome
	// (empty ticket) stays in-memory for LatestClass but is not persisted — it
	// cannot be tied to a ticket. The DB write is deliberately outside the sink
	// lock so it never blocks a concurrent read of the in-memory maps.
	if s.ledger != nil && o.Ticket != "" {
		project := ""
		if s.resolveProject != nil {
			project = s.resolveProject(o.Ticket)
		}
		rec := costledger.CallRecord{
			Project:           project,
			Ticket:            o.Ticket,
			Stage:             o.Stage,
			Model:             o.Model,
			InputTokens:       o.InputTokens,
			OutputTokens:      o.OutputTokens,
			CacheCreateTokens: o.CacheCreateTokens,
			CacheReadTokens:   o.CacheReadTokens,
			DurationMs:        o.Duration.Milliseconds(),
			StartedAt:         o.StartedAt,
		}
		if err := s.ledger.InsertCall(context.Background(), rec); err != nil {
			s.logger.Warn().Err(err).Str("ticket", o.Ticket).Msg("cost ledger insert failed")
		}
	}
}

// Dropped returns how many outcomes were dropped because the channel was full.
func (s *ModelOutcomeSink) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Outcomes returns a flat copy of every retained outcome across all keys. Order
// within a key is oldest-first; ordering across keys is unspecified.
func (s *ModelOutcomeSink) Outcomes() []proxy.ModelCallOutcome {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []proxy.ModelCallOutcome
	for _, hist := range s.byKey {
		out = append(out, hist...)
	}
	return out
}

// LatestClass returns the most recent outcome class for a (ticket, stage) and
// whether one has been recorded. This is the seam a failure marker can read to
// state why a run failed — an auth lapse, an overload, a spend problem — from
// the live boundary rather than after the fact.
func (s *ModelOutcomeSink) LatestClass(ticket, stage string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.latestClass[outcomeKey{ticket: ticket, stage: stage}]
	return c, ok
}
