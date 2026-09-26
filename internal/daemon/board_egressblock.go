package daemon

// SC-5840: the one case where a stage's recorded exit: outage is not an
// outage at all. A host this daemon's own proxy refused by policy closes the
// connection with no TLS alert, so from inside the container it is
// byte-identical to a dead network and the agent honestly records outage.
// The daemon's proxy already knows better — it logs and records the refusal
// at the moment it happens (internal/proxy/server.go) — and this file is the
// one place that turns that record into a classification an outage path can
// read, and the card it posts when it does.

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// EgressBlockWindow is how recent this daemon's own proxy refusal must be to
// explain a stage that reported an outage. Same-clock by construction:
// NetworkEvent.LastSeen is written from the store's clock, so the comparison
// is against time.Now() on the same host. Fifteen minutes covers a run that
// spent minutes retrying a fetch before it died (the SC-5840 incident put six
// containers in twelve minutes) while an hour-old refusal from an unrelated
// container no longer speaks for this run.
const EgressBlockWindow = 15 * time.Minute

// EgressBlock is a policy denial the daemon's own proxy recorded: the host it
// refused and where the allowlist that refused it lives. It is what turns a
// "the network was down" ending into an actionable configuration gap.
type EgressBlock struct {
	Host string
	At   time.Time
	// ConfigFile is "" when the project directory could not be resolved; the
	// card then names the ticket key's project instead of a path.
	ConfigFile string
}

// EgressBlockProbe reports the most recent policy denial that can explain
// this key's run. ok is false for the ordinary case — nothing was refused —
// which is what keeps a genuine outage on its uncharged wait. A nil probe is
// a valid configuration and means "no correlation available" (the package's
// "nil disables" convention), so an unwired daemon keeps the pre-SC-5840
// behaviour.
type EgressBlockProbe func(pmKey string) (EgressBlock, bool)

// NetworkBlockReader is the slice of NetworkEventStore this correlation
// needs. An interface rather than the concrete store so the window and host
// rules are testable without a proxy.
type NetworkBlockReader interface{ Snapshot() []NetworkEvent }

// ConfigFileFor resolves the .humanconfig.yaml whose proxy.domains a key's
// project reads. "" when it cannot be resolved.
type ConfigFileFor func(pmKey string) string

// NewEgressBlockProbe correlates an outage ending with the proxy's own
// refusals. A nil events reader yields a nil probe, so wiring stays optional
// (the package's "nil disables" convention).
func NewEgressBlockProbe(events NetworkBlockReader, configFor ConfigFileFor, now func() time.Time) EgressBlockProbe {
	if events == nil {
		return nil
	}
	return func(pmKey string) (EgressBlock, bool) {
		var found EgressBlock
		var ok bool
		// Newest-last: the store keeps insertion order (networkstore.go), so the
		// last matching row is the most recent one.
		for _, ev := range events.Snapshot() {
			if ev.Source != "proxy" || ev.Status != "block" {
				continue
			}
			if !plausibleHost(ev.Host) {
				continue
			}
			if now().Sub(ev.LastSeen) > EgressBlockWindow {
				continue
			}
			found = EgressBlock{Host: ev.Host, At: ev.LastSeen}
			ok = true
		}
		if !ok {
			return EgressBlock{}, false
		}
		if configFor != nil {
			found.ConfigFile = configFor(pmKey)
		}
		return found, true
	}
}

// plausibleHost reports whether h can be printed as a hostname. The SNI is
// whatever bytes the container sent — parseClientHello (internal/proxy/sni.go)
// validates no charset — and a marker body line is read back by name
// (parsePrefixedLine scans every line for a `resume:` prefix), so an
// implausible host is DISCARDED rather than sanitised: failing back to
// today's outage wait is the safe direction, and printing attacker-chosen
// bytes into a marker is the SC-5592 class.
func plausibleHost(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '*':
		default:
			// Anything else — whitespace, newline, colon, bracket — is rejected: the
			// SNI is container-controlled bytes with no charset validation upstream
			// (parseClientHello), and a marker body line is read back by name
			// (parsePrefixedLine scans for a `resume:` prefix), so an implausible
			// host must never reach a body at all (the SC-5592 class).
			return false
		}
	}
	return true
}

// egressBlockedMarker composes the red a policy denial earns: the stage's own
// *-failed marker with the blocker contract shared/exit-contract.md defines
// for a stop only a person can release. Empty marker + nil order when stage
// has no failed type (the caller then does nothing).
func egressBlockedMarker(stage BoardStage, blk EgressBlock) (marker.Marker, []string) {
	failedType := failedTypeFor(stage)
	if failedType == "" {
		return marker.Marker{}, nil
	}
	line := "- \"" + blk.Host + "\""
	where := blk.ConfigFile
	if where == "" {
		where = "the project's .humanconfig.yaml"
	}
	at := blk.At.UTC().Format(time.RFC3339)
	headline := "this host's proxy refused " + blk.Host +
		", so the run reported an outage that no waiting can clear — add " + line +
		" under proxy.domains in " + where + " and restart the daemon"
	body := "The stage recorded exit: outage, which is what a blocked connection looks like from inside the container — " +
		"the proxy closes it with no TLS alert. This daemon's own proxy recorded refusing the host at " + at +
		", so this is a configuration gap and not a substrate outage: the stage is NOT relaunched, because a policy " +
		"denial does not come back on its own."
	m := Blocker{
		Kind:      "unavailable-dependency",
		Evidence:  "the daemon's proxy refused " + blk.Host + " at " + at + " (proxy block event); the run reported exit: outage",
		Attempted: "nothing to attempt — the connection is refused on this host before it leaves it; the stage was not relaunched because a policy denial does not clear on its own",
		Release:   "add " + line + " under proxy.domains in " + where + ", then restart the daemon and Retry the stage",
	}.addTo(failureMarker(failedType, headline+"\n"+body))
	return m, []string{"reason", "kind", "evidence", "attempted", "release"}
}

// postEgressBlockedFailure posts that red, once. Reports whether the ending is
// handled — true even when the post is suppressed, because a stage another
// actor already redded must not be relaunched either.
func postEgressBlockedFailure(ctx context.Context, pmKey string, stage BoardStage, comments []tracker.Comment,
	blk EgressBlock, post FailedMarkerPoster, logger zerolog.Logger,
) bool {
	if blk.Host == "" || !plausibleHost(blk.Host) || failedTypeFor(stage) == "" || post == nil {
		return false
	}
	if stageAlreadyFailed(comments, stage) {
		logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board egress: the card already says this stage stopped, not repeating it")
		return true
	}
	m, order := egressBlockedMarker(stage, blk)
	if err := post(ctx, pmKey, markerBody(m, order...)); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).Str("host", blk.Host).
			Msg("board egress: cannot post the policy-blocked red")
		return false
	}
	logger.Warn().Str("pm", pmKey).Str("stage", string(stage)).Str("host", blk.Host).
		Msg("board egress: reclassified an outage as a policy-blocked host; card redded once, no relaunch")
	return true
}

// commenterPoster adapts a tracker.Commenter to the FailedMarkerPoster the
// reconcile pass already supplies, so the three call sites share one composer.
func commenterPoster(c tracker.Commenter) FailedMarkerPoster {
	return func(ctx context.Context, pmKey, body string) error {
		_, err := c.AddComment(ctx, pmKey, body)
		return err
	}
}
