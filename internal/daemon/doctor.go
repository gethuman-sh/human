package daemon

import (
	"context"
	"sync"
	"time"
)

// defaultCheckTimeout bounds a single doctor check so one wedged probe (a
// hung docker socket, a stalled tracker call) can never hang the runner or
// the UIs polling it.
const defaultCheckTimeout = 5 * time.Second

// DoctorCheckDef is one substrate probe: cheap, side-effect free, and honest
// about what is broken. Run returns ok plus a detail line — for failures the
// detail must name the fix, not just the symptom, because it becomes the LED
// tooltip and the launch-refusal message.
type DoctorCheckDef struct {
	ID      string
	Name    string
	Timeout time.Duration // zero means defaultCheckTimeout
	Run     func(ctx context.Context) (ok bool, detail string)
}

// DoctorCheck is the wire form of one check result.
type DoctorCheck struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// DoctorData is the wire form of a doctor run: the substrate's health as one
// bool for the LED plus the per-check detail for tooltips and `human doctor`.
type DoctorData struct {
	Healthy   bool          `json:"healthy"`
	CheckedAt string        `json:"checkedAt"`
	Checks    []DoctorCheck `json:"checks"`
}

// DoctorRunner runs the check suite and caches the results, so the desktop
// LED can poll every few seconds while the (potentially slower) probes run at
// most once per staleness window. Refresh is lazy — no background goroutine to
// manage; the first stale read pays for the run.
type DoctorRunner struct {
	checks []DoctorCheckDef

	mu        sync.Mutex
	last      DoctorData
	lastRunAt time.Time
}

// NewDoctorRunner creates a runner over the given checks. Check order is
// presentation order.
func NewDoctorRunner(checks []DoctorCheckDef) *DoctorRunner {
	return &DoctorRunner{checks: checks}
}

// Results returns the check results, re-running the suite when the cache is
// older than maxAge (zero forces a live run). A nil runner reports healthy
// with no checks — the feature is disabled, and a disabled doctor must never
// block work.
func (d *DoctorRunner) Results(ctx context.Context, maxAge time.Duration) DoctorData {
	if d == nil {
		return DoctorData{Healthy: true}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if maxAge > 0 && !d.lastRunAt.IsZero() && time.Since(d.lastRunAt) < maxAge {
		return d.last
	}
	d.last = d.run(ctx)
	d.lastRunAt = time.Now()
	return d.last
}

// run executes every check with its own timeout; callers hold d.mu. The suite
// is small and each probe bounded, so sequential execution keeps results
// deterministic without meaningful latency cost.
func (d *DoctorRunner) run(ctx context.Context) DoctorData {
	data := DoctorData{Healthy: true, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, def := range d.checks {
		timeout := def.Timeout
		if timeout == 0 {
			timeout = defaultCheckTimeout
		}
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		ok, detail := def.Run(checkCtx)
		cancel()
		if !ok {
			data.Healthy = false
		}
		data.Checks = append(data.Checks, DoctorCheck{ID: def.ID, Name: def.Name, OK: ok, Detail: detail})
	}
	return data
}

// Blockers returns the failing subset of the given launch-critical check IDs,
// from a briefly-cached run: an agent launch on a substrate the doctor knows
// is broken would burn minutes to rediscover the same failure, so the launch
// path refuses with the check's own message instead.
func (d *DoctorRunner) Blockers(ctx context.Context, criticalIDs []string) []DoctorCheck {
	if d == nil {
		return nil
	}
	critical := make(map[string]bool, len(criticalIDs))
	for _, id := range criticalIDs {
		critical[id] = true
	}
	var out []DoctorCheck
	for _, c := range d.Results(ctx, 30*time.Second).Checks {
		if !c.OK && critical[c.ID] {
			out = append(out, c)
		}
	}
	return out
}
