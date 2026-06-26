package monarch

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// senderBufSize bounds the in-flight event ring. When full the oldest event is
// dropped so a monarch outage can never back-pressure daemon work.
const senderBufSize = 1024

const (
	dialTimeout    = 5 * time.Second
	backoffInitial = 1 * time.Second
	backoffMax     = 30 * time.Second
)

// Sender streams Events to a monarch server over a single reconnecting TCP
// connection. Send never blocks the caller: when the buffer is full the OLDEST
// queued event is dropped (freshest swarm state wins). A monarch outage only
// drops events; it never slows daemon work.
type Sender struct {
	addr     string
	ch       chan Event
	logger   zerolog.Logger
	done     chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
}

// NewSender creates a Sender and starts the background dial/send goroutine. The
// goroutine runs until ctx is cancelled or Close is called.
func NewSender(ctx context.Context, addr string, logger zerolog.Logger) *Sender {
	s := &Sender{
		addr:   addr,
		ch:     make(chan Event, senderBufSize),
		logger: logger,
		done:   make(chan struct{}),
		quit:   make(chan struct{}),
	}
	go s.run(ctx)
	return s
}

// Send enqueues an event for async transmission. It never blocks: a full buffer
// drops the oldest queued event then enqueues the new one. The channel is never
// closed, so Send is safe to call concurrently with — and after — Close.
func (s *Sender) Send(e Event) {
	for {
		select {
		case s.ch <- e:
			return
		case <-s.quit:
			return
		default:
			// Buffer full: drop the oldest queued event, then retry the enqueue
			// so the freshest swarm state is preserved.
			select {
			case <-s.ch:
				s.logger.Warn().Msg("monarch sender buffer full, dropping oldest event")
			default:
			}
		}
	}
}

func (s *Sender) run(ctx context.Context) {
	defer close(s.done)
	backoff := backoffInitial
	for {
		if s.stopping(ctx) {
			return
		}
		conn, err := net.DialTimeout("tcp", s.addr, dialTimeout)
		if err != nil {
			s.logger.Debug().Err(err).Str("addr", s.addr).Msg("monarch dial failed, backing off")
			if !s.sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = backoffInitial
		s.pump(ctx, conn)
	}
}

// pump writes queued events to conn until a write fails or the sender is
// stopping, at which point it returns so run reconnects.
func (s *Sender) pump(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quit:
			return
		case e := <-s.ch:
			data, err := json.Marshal(e)
			if err != nil {
				s.logger.Warn().Err(err).Msg("monarch failed to marshal event")
				continue
			}
			data = append(data, '\n')
			if _, err := conn.Write(data); err != nil {
				s.logger.Debug().Err(err).Msg("monarch write failed, reconnecting")
				return
			}
		}
	}
}

// stopping reports whether the sender should exit its dial loop.
func (s *Sender) stopping(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-s.quit:
		return true
	default:
		return false
	}
}

// sleep waits for d, returning false if the sender should stop instead.
func (s *Sender) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.quit:
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	next := d * 2
	if next > backoffMax {
		return backoffMax
	}
	return next
}

// Close signals the sender to stop and waits for the background goroutine to
// finish. It is idempotent and safe to call after ctx cancellation.
func (s *Sender) Close() {
	s.quitOnce.Do(func() { close(s.quit) })
	<-s.done
}
