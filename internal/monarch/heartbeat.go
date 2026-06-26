package monarch

import (
	"context"
	"time"
)

// DefaultHeartbeatInterval is how often an otherwise-idle daemon announces its
// presence to monarch. The console's live-presence window is a small multiple
// of this so a single dropped heartbeat does not make a daemon vanish.
const DefaultHeartbeatInterval = 15 * time.Second

// heartbeatSink is the slice of the sender the heartbeat loop needs; kept narrow
// so the loop is trivially unit-testable with a fake.
type heartbeatSink interface {
	Send(e Event)
}

// StartHeartbeat sends a heartbeat event immediately and then every interval, so
// monarch shows the daemon as connected even when no agent is running. It runs
// until ctx is done. Sends are best-effort (the sender drops when monarch is
// unreachable), so a heartbeat never blocks the daemon.
func StartHeartbeat(ctx context.Context, sink heartbeatSink, daemonID, team string, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	beat := func() {
		sink.Send(Event{
			Type:     EventHeartbeat,
			Team:     team,
			DaemonID: daemonID,
			State:    StateIdle,
			TS:       time.Now().UTC(),
		})
	}
	go func() {
		// Beat once up front so the daemon appears without a full interval's delay.
		beat()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				beat()
			}
		}
	}()
}
