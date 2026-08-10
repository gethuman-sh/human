package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// OutageWaitBound is how long a stage may keep waiting on a substrate it cannot
// reach before the wait stops being the machine's problem and becomes a
// person's. It is deliberately generous and measured in ELAPSED TIME rather
// than attempts: SC-2307's rule is that an outage costs time and nothing else,
// so attempts must stay free — but time is the one thing that is not free, and
// an outage that never ends (a revoked token, a closed account, an approval
// that will not be granted again) is indistinguishable from one that will
// return except by how long it has lasted (SC-2851). Exported so tests can
// shorten it.
var OutageWaitBound = 6 * time.Hour

// outageRunSince returns when the card's CURRENT outage run began: the oldest
// marker in the unbroken run of *-outage markers that ends the stage's history.
// Any non-outage marker in the stage (a started marker from a relaunch that got
// through, a failure, a done) ends the run, so a substrate that came back and
// went down again is timed from the second outage, not the first.
//
// The anchor is read off the tracker rather than kept in daemon state on
// purpose: the thread is the one record that survives a daemon restart, a
// handover to a peer daemon, and the state db being wiped — all of which the
// wait must outlive to be measurable at all.
func outageRunSince(comments []tracker.Comment, stage BoardStage) (time.Time, bool) {
	var run []tracker.Comment
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage {
			continue
		}
		if st != BoardOutage {
			// A non-outage marker of this stage ends any earlier run: keep only the
			// outage markers newer than it.
			run = newerThan(run, c)
			continue
		}
		run = append(run, c)
	}
	var since tracker.Comment
	var have bool
	for _, c := range run {
		if !have || commentNewer(since, c) {
			since, have = c, true
		}
	}
	return since.Created, have
}

// newerThan drops every comment that is not strictly newer than cutoff. Used to
// discard outage markers from a run the tracker thread shows was already broken.
func newerThan(comments []tracker.Comment, cutoff tracker.Comment) []tracker.Comment {
	kept := comments[:0]
	for _, c := range comments {
		if commentNewer(c, cutoff) {
			kept = append(kept, c)
		}
	}
	return kept
}

// outageHandoverBody composes the red that ends an unending wait. It names what
// could not be reached and for how long — the two facts a person needs before
// they can do the one thing the machine cannot: decide whether the substrate is
// ever coming back. The first body line is the headline the card badge reads
// (failureReason), so the card itself says why it needs attention.
func outageHandoverBody(stage BoardStage, reason string, waited time.Duration, since time.Time) string {
	failedType := failedTypeFor(stage)
	if failedType == "" {
		return ""
	}
	what := strings.TrimSpace(reason)
	if what == "" {
		what = "the substrate it depends on"
	}
	return markerBody(failureMarker(failedType,
		"waited "+waited.Round(time.Minute).String()+" for the substrate to come back and it never did — this needs a person: "+what+"\n"+
			"unreachable since "+since.UTC().Format(time.RFC3339)+". No retry budget was spent on the wait, so a Retry has every attempt available once the substrate is back."))
}

// handOverOutage reds a card whose outage outlived OutageWaitBound so a person
// is finally told. It charges nothing: SC-2307's rule that an outage never
// spends the budget a genuine failure needs is untouched by this — the wait is
// ended, not reclassified as a failure of the work.
func handOverOutage(ctx context.Context, pmKey string, derived BoardCard, postFailed FailedMarkerPoster, waited time.Duration, since time.Time, daemonID string, logger zerolog.Logger) bool {
	body := outageHandoverBody(derived.Stage, derived.Error, waited, since)
	if body == "" {
		return false
	}
	if err := postFailed(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).
			Msg("board reconcile: cannot hand an unending outage to a person, leaving the card waiting")
		return false
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(derived.Stage)).Dur("waited", waited).
		Msg("board reconcile: outage outlasted the wait bound, card handed to a human")
	return true
}

// handleOutageExit deals with a stage exit that reported the substrate was
// down — whether recorded via the retry policy's ExitOutage (SC-2307) or
// recognised here from the hook errorType / model-boundary class alone (the
// SC-2856 incident: a refusal that kills the agent before it records an exit)
// — and reports whether it handled it. An outage is not a failure: it posts a
// distinct *-outage marker so the card reads "paused" rather than red, and
// does NOT relaunch here — the durable reconcile pass owns the backoff, with
// the retry budget untouched.
//
// kind/reason are classifyUnavailability's verdict: kind == endingPaused
// routes here even when nothing was recorded; reason is the substrate phrase
// for the card face (falls back to "the substrate it depends on" when empty —
// the plain recorded-outage case with no signal reason).
//
// Split out of handleBoardAgentExit so the say-once guard costs this function's
// complexity budget rather than that one's.
func handleOutageExit(ctx context.Context, exit RunExit, commenter tracker.Commenter, deps FailureDeps, kind endingKind, reason string) bool {
	logger := deps.Logger
	outageType := outageTypeFor(exit.Stage)
	if outageType == "" || (!deps.Retry.recordedOutage(exit.PMKey, exit.Stage) && kind != endingPaused) {
		return false
	}
	body := markerBody(pausedOutageMarker(outageType, deps.Diagnose, exit.AgentName, exit.ErrorType, reason))
	// Say it once and leave it standing: every relaunch that re-hits the same
	// outage lands here, so re-posting an identical marker would spam the ticket
	// for as long as the substrate stays down (SC-2851). The standing marker also
	// anchors how long the wait has lasted (outageRunSince), so leaving it in
	// place is what makes the wait measurable at all.
	if outageAlreadyStated(exit.Comments, exit.Stage, body) {
		logger.Info().Str("agent", exit.AgentName).Str("pm", exit.PMKey).
			Msg("board failure: the card already says the substrate is down, not repeating it")
		return true
	}
	if _, err := commenter.AddComment(ctx, exit.PMKey, body); err != nil {
		logger.Warn().Err(err).Str("agent", exit.AgentName).Msg("board failure: cannot post outage marker")
	}
	return true
}

// pausedOutageMarker composes the house-style paused statement: the substrate
// reason, an optional machine-readable resume field (when the diagnosis names
// a stated recovery time), and the do-nothing reassurance. reason defaults to
// "the substrate it depends on" when empty — the recorded-outage case with no
// classified signal reason.
func pausedOutageMarker(outageType string, diagnose BoardFailureDiagnoser, agentName, errorType, reason string) marker.Marker {
	what := strings.TrimSpace(reason)
	if what == "" {
		what = "the substrate it depends on"
	}
	m := marker.Marker{
		Type: outageType,
		Body: "paused — " + what + "\nThe work is written and safe on the ticket. It continues automatically when " +
			what + " clears. Nothing to do.",
	}
	// A stated recovery time is the one machine-readable fact in this marker
	// (parseResumeLine reads it back to bound the wait), so it is a field rather
	// than a line of the prose it used to sit in the middle of.
	if resume, ok := resumeTimeFromDiagnosis(diagnose, agentName, errorType); ok {
		m.Fields = fields("resume", resume.Format(time.RFC3339))
	}
	return m
}

// resumeTimeFromDiagnosis runs the diagnoser (when wired) and scans its
// headline+detail for a stated recovery time. ok is false when there is no
// diagnoser, no diagnosis, or no time could be parsed out of it.
func resumeTimeFromDiagnosis(diagnose BoardFailureDiagnoser, agentName, errorType string) (time.Time, bool) {
	if diagnose == nil {
		return time.Time{}, false
	}
	d := diagnose(agentName, errorType)
	return parseResumeTime(d.Headline+"\n"+d.Detail, time.Now(), time.UTC)
}

// outageAlreadyStated reports whether the card's newest marker for the stage
// already says exactly what is about to be posted. Every relaunched agent that
// re-hits the same outage exits the same way, so without this the card collects
// one identical "machine is down" marker per reconcile tick — hundreds over a
// weekend, and on trackers whose comments and tickets share an id sequence, a
// backlog that looks busier than the work in it. Saying a thing once and
// leaving it current is the rule everywhere else in the pipeline; this is that
// rule reaching the outage path (SC-2851).
//
// A CHANGED body still posts: the substrate that is down is part of the
// statement, so a different reason is different news, not a repeat.
func outageAlreadyStated(comments []tracker.Comment, stage BoardStage, body string) bool {
	state, latest := latestStateInStage(comments, stage)
	if state != BoardOutage {
		return false
	}
	return withoutSignature(latest.Body) == withoutSignature(body)
}

// withoutSignature strips a marker's provenance — the structured machine:/build:
// fields and the legacy trailing daemon: line — so two markers saying the same
// thing compare equal even when posted by different machines on different builds.
// Without this the "state it once" dedup breaks the moment two daemons on
// different builds hit the same outage, and the ticket refills with near-dupes.
func withoutSignature(body string) string {
	var kept []string
	for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, DaemonLinePrefix) ||
			strings.HasPrefix(trimmed, marker.MachineField+":") ||
			strings.HasPrefix(trimmed, marker.BuildField+":") {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
