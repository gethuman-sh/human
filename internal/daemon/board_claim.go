package daemon

import (
	"context"
	stderrors "errors"
	"math/big"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ClaimHeader marks a daemon's intent to launch a stage, posted BEFORE the
// stage's *-started marker. When several daemons watch the same board they can
// decide to launch the same stage at the same instant; each posts a claim, then
// re-reads the thread, and the claim with the lowest server-assigned comment ID
// wins (SC-660 rule 2). A loser starts nothing and leaves the launch to the
// winner. Like PlanCommentHeader and CloseFailedHeader it is content, not a
// stage transition — it MUST never join orderedMarkerSpecs, so ClassifyMarker
// never sees it and it never moves a card.
const ClaimHeader = "[human:claim]"

// ClaimStagePrefix is the marker-body line naming the stage a claim is for, so
// concurrent claims on different stages of the same ticket never contend.
const ClaimStagePrefix = "stage:"

// ClaimTTL bounds how long an unfulfilled claim (one that never produced the
// stage's *-started marker) blocks later claimants. A daemon that posts a claim
// and then crashes before launching would otherwise wedge the stage forever;
// past the TTL its claim is treated as dead and ignored, so a fresh claimant can
// win. The winner posts *-started within one AddComment of winning, so a live
// claim is fulfilled in seconds — the TTL only has to outlast tracker round-trip
// latency, not the agent run. A package var so tests can shorten it.
var ClaimTTL = 5 * time.Minute

// claimNow is the clock the claim gate reads, indirected so tests can pin it.
var claimNow = time.Now

// ErrClaimLost is the sentinel for a stage this daemon did not win: a live claim
// with a lower comment id holds it, so the daemon that posted it is starting the
// work and this one must not. A sentinel rather than a plain error because the
// two sides of the race mean different things to different callers — a machine
// driven chain or retry backs off without a word (the winner's own markers move
// the card within seconds), while a person who asked for the launch is told why
// nothing started. Mirrors ErrLaunchGateRefused (SC-5108) for the other launch
// that starts nothing (SC-5094).
var ErrClaimLost = stderrors.New("another daemon holds the winning claim for this stage")

// claimWinner names the claim that beat this daemon's, so a refusal can say who
// is starting the work instead of only that nothing started. Zero when this
// daemon won, or when its own claim could not be identified at all.
type claimWinner struct {
	ID       string // the winning claim's server comment id
	DaemonID string // the daemon that posted it; empty for an unsigned claim
}

// winClaim posts this daemon's claim for a stage, re-reads the thread, and
// reports whether this daemon holds the lowest live claim — the winner that may
// proceed to launch. A loser starts nothing; whether that is reported depends on
// who asked for the launch (claimLostError). It is the cross-daemon arbiter for
// rule 2: N daemons that decide to launch the same stage at the same instant
// each post a claim, and exactly one — the lowest server comment ID — wins,
// deterministically and without new infrastructure.
//
// Claim arbitration is keyed on the daemon identity (rule 1). An un-provisioned
// daemon (empty DaemonID) has no identity to contend with, so it skips the claim
// entirely and launches directly — single-daemon behavior, unchanged, mirroring
// the signing decorator's empty-machine no-op. The claim body is signed at the
// commenter choke point, so the daemon id round-trips as the marker's machine:
// field for claimWon to read back.
func (d BoardTransitionDeps) winClaim(ctx context.Context, pmKey string, stage BoardStage) (won bool, winner claimWinner, err error) {
	if strings.TrimSpace(d.DaemonID) == "" {
		return true, claimWinner{}, nil
	}
	body := markerBody(marker.Marker{Type: MarkerClaim, Fields: fields("stage", string(stage))})
	posted, err := d.Commenter.AddComment(ctx, pmKey, body)
	if err != nil {
		return false, claimWinner{}, errors.WrapWithDetails(err, "posting stage claim", "pm", pmKey, "stage", string(stage))
	}
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return false, claimWinner{}, errors.WrapWithDetails(err, "re-reading thread after claim", "pm", pmKey, "stage", string(stage))
	}
	myID := ""
	if posted != nil {
		myID = posted.ID
	}
	won, winner = claimWon(comments, stage, myID, d.DaemonID, claimNow())
	return won, winner, nil
}

// claimWon reports whether this daemon's claim is the lowest live claim for the
// stage and, when it is not, which claim beat it. myClaimID is the server id
// echoed by AddComment; when a backend does not echo one it is recovered from
// the thread as the newest claim carrying this daemon's id. A claim that cannot
// be identified refuses to win, so a lost id never risks a double launch (a
// later reconcile pass retries).
//
// A daemon never contends with ITSELF. Its own earlier claims for the stage are
// skipped, because the claim it just posted is the only one it will ever launch
// — an older one is a leftover, not a rival. Without that, two launches that
// failed after claiming (a *-failed marker does not fulfil a claim, so both
// stayed live for ClaimTTL) made the next attempt lose the race to its own
// leftovers, and the stage was unusable for five minutes (SC-5094). This is the
// daemon identity rule 1 the arbitration always claimed to key on.
func claimWon(comments []tracker.Comment, stage BoardStage, myClaimID, myDaemonID string, now time.Time) (won bool, winner claimWinner) {
	if strings.TrimSpace(myClaimID) == "" {
		myClaimID = newestOwnClaimID(comments, stage, myDaemonID)
	}
	if myClaimID == "" {
		return false, claimWinner{}
	}
	live := liveClaims(comments, stage, now)
	mineLive := false
	for _, c := range live {
		if c.ID == myClaimID {
			mineLive = true
			break
		}
	}
	if !mineLive {
		return false, claimWinner{}
	}
	beaten := claimWinner{}
	for _, c := range live {
		if c.ID == myClaimID {
			continue
		}
		if myDaemonID != "" && ParseDaemonID(c.Body) == myDaemonID {
			continue // my own earlier claim: a leftover of mine, not a rival
		}
		if !claimIDLess(c.ID, myClaimID) {
			continue
		}
		if beaten.ID == "" || claimIDLess(c.ID, beaten.ID) {
			beaten = claimWinner{ID: c.ID, DaemonID: ParseDaemonID(c.Body)}
		}
	}
	if beaten.ID != "" {
		return false, beaten
	}
	return true, claimWinner{}
}

// liveClaims returns the stage's claims that are neither fulfilled nor expired.
// A claim is fulfilled once a *-started marker for the stage lands at or after
// it (the launch it represents happened), and expired once ClaimTTL elapses with
// no such marker (the claimant crashed before launching). Only live claims
// contend, so a fulfilled or dead claim never wedges the stage. Whether a live
// claim contends with a PARTICULAR daemon is claimWon's question, not this one's.
func liveClaims(comments []tracker.Comment, stage BoardStage, now time.Time) []tracker.Comment {
	startedComment, hasStarted := latestStartedFor(comments, stage)
	var live []tracker.Comment
	for _, c := range comments {
		st, ok := claimStage(c.Body)
		if !ok || st != stage {
			continue
		}
		if hasStarted && !commentNewer(c, startedComment) {
			continue // fulfilled: the stage's launch is newer-or-equal to this claim
		}
		if now.Sub(c.Created) > ClaimTTL {
			continue // expired: claimant crashed before posting *-started
		}
		live = append(live, c)
	}
	return live
}

// claimLostError renders a lost race as something a person can act on. It wraps
// ErrClaimLost so a machine-driven caller can recognise and swallow it, and it
// names the winning daemon in the MESSAGE rather than only in the details,
// because the board surfaces the cause chain alone (desktop daemonCause).
func claimLostError(pmKey string, stage BoardStage, winner claimWinner) error {
	switch {
	case winner.ID == "":
		return errors.WrapWithDetails(ErrClaimLost,
			"this machine's claim on the stage could not be identified, so nothing was started here",
			"pm", pmKey, "stage", string(stage))
	case winner.DaemonID == "":
		return errors.WrapWithDetails(ErrClaimLost,
			"another daemon claimed this stage first and is starting it; nothing started here",
			"pm", pmKey, "stage", string(stage), "winning claim", winner.ID)
	default:
		return errors.WrapWithDetails(ErrClaimLost,
			"daemon %s claimed this stage first and is starting it; nothing started here",
			"winning daemon", winner.DaemonID, "pm", pmKey, "stage", string(stage), "winning claim", winner.ID)
	}
}

// latestStartedFor returns the newest *-started marker comment for a stage, reusing
// ClassifyMarker so "started" is defined exactly as the board derives it. ok is
// false when the stage has never been launched.
func latestStartedFor(comments []tracker.Comment, stage BoardStage) (tracker.Comment, bool) {
	var newest tracker.Comment
	found := false
	for _, c := range comments {
		st, state, ok := ClassifyMarker(c.Body)
		if !ok || st != stage || state != BoardRunning {
			continue
		}
		if !found || commentNewer(c, newest) {
			newest = c
			found = true
		}
	}
	return newest, found
}

// newestOwnClaimID recovers this daemon's claim id from the thread when the
// backend's AddComment did not echo one: the newest claim for the stage carrying
// this daemon's id is ours.
func newestOwnClaimID(comments []tracker.Comment, stage BoardStage, daemonID string) string {
	var newest tracker.Comment
	found := false
	for _, c := range comments {
		st, ok := claimStage(c.Body)
		if !ok || st != stage || ParseDaemonID(c.Body) != daemonID {
			continue
		}
		if !found || commentNewer(c, newest) {
			newest = c
			found = true
		}
	}
	if !found {
		return ""
	}
	return newest.ID
}

// claimStage reports whether a comment body is a claim marker and, if so, which
// stage it claims. A claim with no stage: line yields an empty stage, which
// matches no target stage and is therefore skipped by the arbiter.
func claimStage(body string) (BoardStage, bool) {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, ClaimHeader) {
		return "", false
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), ClaimStagePrefix); ok {
			return BoardStage(strings.TrimSpace(rest)), true
		}
	}
	return "", true
}

// claimIDLess reports whether comment id a sorts before b under the "lowest
// server comment ID wins" arbitration. Server comment ids are numeric on the
// backends human targets, so when both parse as integers they are compared
// numerically ("9" < "10"); a non-numeric id falls back to a byte-wise compare,
// still a deterministic total order every daemon computes identically from the
// same thread. big.Int keeps arbitrarily large ids exact.
func claimIDLess(a, b string) bool {
	ai, aok := new(big.Int).SetString(strings.TrimSpace(a), 10)
	bi, bok := new(big.Int).SetString(strings.TrimSpace(b), 10)
	if aok && bok {
		return ai.Cmp(bi) < 0
	}
	return a < b
}

// commentNewer reports whether comment a is more recent than b under the board's
// total order: primary key Created (later wins), tie broken by the server-assigned
// comment ID (higher id = later post wins), reusing claimIDLess's big.Int/byte-wise
// order. Same-second markers therefore resolve on the monotonic id rather than the
// tracker's unstable slice order (SC-1701). Equal Created with equal or absent ids
// yields false, preserving the strict-.After first-seen-wins behaviour for backends
// that do not populate an id.
func commentNewer(a, b tracker.Comment) bool {
	if a.Created.Equal(b.Created) {
		return claimIDLess(b.ID, a.ID)
	}
	return a.Created.After(b.Created)
}
