package daemon

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultCheckTimeout bounds a single doctor check so one wedged probe (a
// hung docker socket, a stalled tracker call) can never hang the runner or
// the UIs polling it.
const defaultCheckTimeout = 5 * time.Second

// transientFailThreshold is how many consecutive failures a transient check
// must post before its failure counts as persistent. A check that reaches out
// to another tool (a tracker API, a vault agent) can fail for a moment and
// then succeed; one failed attempt is a blip, not a fault (SC-1991). Until the
// streak reaches this bound the failure is reported as SeverityTransient —
// visible, but never a blocking, system-level alarm.
const transientFailThreshold = 2

// Check severities classify one failing probe by its real consequence, so the
// verdict and the UIs can tell a hard stop from a note worth knowing (SC-1991):
//   - SeverityOK       the check passed.
//   - SeverityBlocking a gating check is persistently failing — new work
//     genuinely cannot start. This is the only severity that must stay loud.
//   - SeverityDegraded a non-gating check is failing — worth knowing, but it
//     stops nothing, so it must be shown without being announced as an outage.
//   - SeverityTransient a transient check failed, but not yet often enough to
//     be called broken ("failed once just now", not "persistently broken").
const (
	SeverityOK        = "ok"
	SeverityBlocking  = "blocking"
	SeverityDegraded  = "degraded"
	SeverityTransient = "transient"
)

// DoctorCheckDef is one substrate probe: cheap, side-effect free, and honest
// about what is broken. Run returns ok plus a detail line — for failures the
// detail must name the fix, not just the symptom, because it becomes the LED
// tooltip and the launch-refusal message.
type DoctorCheckDef struct {
	ID      string
	Name    string
	Timeout time.Duration // zero means defaultCheckTimeout
	// Gating marks a check whose failure genuinely prevents new work from
	// starting. Only gating failures may be presented as a hard stop; a
	// non-gating failure is advisory. Kept in lockstep with the launch gate's
	// LaunchCriticalChecks so the report and the refusal path share one notion
	// of "blocks work" (SC-1991).
	Gating bool
	// Transient marks a check that reaches out to another tool and can fail for
	// a moment and then recover. Its first failures are debounced (see
	// transientFailThreshold) so a blip is never raised as a system fault.
	Transient bool
	Run       func(ctx context.Context) (ok bool, detail string)
}

// DoctorCheck is the wire form of one check result.
type DoctorCheck struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Gating echoes the def's Gating flag so UIs can render a failing check with
	// its real weight without re-deriving the launch-critical set.
	Gating bool `json:"gating,omitempty"`
	// Severity is the failure's classification (see the Severity* constants). A
	// passing check is SeverityOK.
	Severity string `json:"severity,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// DoctorData is the wire form of a doctor run: the substrate's health for the
// LED plus the per-check detail for tooltips and `human doctor`.
type DoctorData struct {
	// Healthy is true only when every check passes — it drives the "all green"
	// state. It is NOT the work-can-start verdict; a failing non-gating or
	// transient check makes Healthy false without blocking anything.
	Healthy bool `json:"healthy"`
	// Blocked is the work-can-start verdict: true only when a gating check is
	// persistently failing. This — not Healthy — is what may be announced as a
	// hard stop (SC-1991), keeping harmless failures from raising an outage
	// alarm while real blockers stay loud.
	Blocked bool `json:"blocked"`
	// Summary describes the run in its real consequence: what (if anything)
	// blocks work, and which failures are merely advisory.
	Summary   string        `json:"summary,omitempty"`
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
	// failStreaks counts each check's consecutive failures across runs, so a
	// transient check's blip can be told from a persistent fault (SC-1991). A
	// passing run resets the check's streak to zero.
	failStreaks map[string]int
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
// deterministic without meaningful latency cost. It classifies each failure by
// its real consequence and derives the work-can-start verdict from the gating
// subset alone, so a harmless or momentary failure never masquerades as an
// outage (SC-1991).
func (d *DoctorRunner) run(ctx context.Context) DoctorData {
	if d.failStreaks == nil {
		d.failStreaks = make(map[string]int)
	}
	data := DoctorData{Healthy: true, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, def := range d.checks {
		timeout := def.Timeout
		if timeout == 0 {
			timeout = defaultCheckTimeout
		}
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		ok, detail := def.Run(checkCtx)
		cancel()

		if ok {
			delete(d.failStreaks, def.ID)
		} else {
			d.failStreaks[def.ID]++
			data.Healthy = false
		}
		severity := classifySeverity(def, ok, d.failStreaks[def.ID])
		if severity == SeverityBlocking {
			data.Blocked = true
		}
		data.Checks = append(data.Checks, DoctorCheck{
			ID: def.ID, Name: def.Name, OK: ok,
			Gating: def.Gating, Severity: severity, Detail: detail,
		})
	}
	data.Summary = summarizeDoctor(data.Checks)
	return data
}

// classifySeverity maps one check outcome to its real consequence. A transient
// check that has only just started failing is held at SeverityTransient until
// its streak proves the fault persistent — so a blip is never escalated to a
// blocking, system-level alarm (SC-1991).
func classifySeverity(def DoctorCheckDef, ok bool, streak int) string {
	if ok {
		return SeverityOK
	}
	if def.Transient && streak < transientFailThreshold {
		return SeverityTransient
	}
	if def.Gating {
		return SeverityBlocking
	}
	return SeverityDegraded
}

// summarizeDoctor renders the run's verdict in its real consequence: what (if
// anything) blocks new work, and which failures are merely worth knowing. It
// deliberately never calls a non-blocking failure an outage.
func summarizeDoctor(checks []DoctorCheck) string {
	var blocking, degraded, transient []string
	for _, c := range checks {
		switch c.Severity {
		case SeverityBlocking:
			blocking = append(blocking, c.Name)
		case SeverityDegraded:
			degraded = append(degraded, c.Name)
		case SeverityTransient:
			transient = append(transient, c.Name)
		}
	}
	advisory := append(append([]string{}, degraded...), transient...)
	switch {
	case len(blocking) > 0:
		s := "new work is blocked: " + strings.Join(blocking, ", ") + " failing"
		if len(advisory) > 0 {
			s += " (also, non-blocking: " + strings.Join(advisory, ", ") + ")"
		}
		return s
	case len(advisory) > 0:
		return "work can start; " + strconv.Itoa(len(advisory)) + " advisory issue(s), nothing blocked: " + strings.Join(advisory, ", ")
	default:
		return "all systems go"
	}
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
