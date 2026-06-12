package gui

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/gethuman-sh/human/internal/claude/monitor"
)

// SnapshotFetcher is the slice of *monitor.Monitor the poller needs.
// Injected so tests can fabricate snapshots without real discovery.
type SnapshotFetcher interface {
	FetchFull(ctx context.Context) *monitor.Snapshot
}

const (
	// pollInterval mirrors the TUI's fullTick cadence so both surfaces
	// show the same freshness.
	pollInterval = 2 * time.Second
	// staleAfter is how old a cached snapshot may be before an on-demand
	// HTTP GET triggers a fresh fetch instead of serving the cache.
	staleAfter = 3 * time.Second
	// fetchTimeout bounds one full fetch (discovery + daemon RPCs).
	fetchTimeout = 30 * time.Second
)

// Poller assembles dashboard snapshots from a Monitor. It polls on the
// TUI cadence, but only while at least one WebSocket client is attached —
// an idle daemon must not burn discovery cycles for nobody.
type Poller struct {
	fetcher  SnapshotFetcher
	hostname string

	mu        sync.Mutex
	cached    SnapshotDTO
	fetchedAt time.Time
	clients   int
	wake      chan struct{}

	// broadcast is invoked with each fresh snapshot while clients are
	// attached; the WS hub wires itself in here.
	broadcast func(SnapshotDTO)
}

// NewPoller creates a Poller around a snapshot fetcher.
func NewPoller(fetcher SnapshotFetcher) *Poller {
	hostname, _ := os.Hostname()
	return &Poller{
		fetcher:  fetcher,
		hostname: hostname,
		wake:     make(chan struct{}, 1),
	}
}

// SetBroadcast registers the per-snapshot push callback. Must be called
// before Run.
func (p *Poller) SetBroadcast(fn func(SnapshotDTO)) { p.broadcast = fn }

// Run drives the poll loop until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-ticker.C:
		}
		if p.clientCount() == 0 {
			continue
		}
		dto := p.fetch(ctx)
		if p.broadcast != nil {
			p.broadcast(dto)
		}
	}
}

// Snapshot serves the cache when fresh, fetching on demand otherwise.
// This keeps a plain GET /api/snapshot working without any WS client.
func (p *Poller) Snapshot(ctx context.Context) (SnapshotDTO, error) {
	p.mu.Lock()
	if time.Since(p.fetchedAt) < staleAfter {
		dto := p.cached
		p.mu.Unlock()
		return dto, nil
	}
	p.mu.Unlock()
	return p.fetch(ctx), nil
}

// ClientAttached signals that a WS client connected; wakes the loop so
// the first snapshot arrives without waiting a full tick.
func (p *Poller) ClientAttached() {
	p.mu.Lock()
	p.clients++
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// ClientDetached signals that a WS client disconnected.
func (p *Poller) ClientDetached() {
	p.mu.Lock()
	if p.clients > 0 {
		p.clients--
	}
	p.mu.Unlock()
}

func (p *Poller) clientCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clients
}

func (p *Poller) fetch(ctx context.Context) SnapshotDTO {
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	snap := p.fetcher.FetchFull(fetchCtx)
	dto := ToSnapshotDTO(snap, p.hostname)
	p.mu.Lock()
	p.cached = dto
	p.fetchedAt = time.Now()
	p.mu.Unlock()
	return dto
}
