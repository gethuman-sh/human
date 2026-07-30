package daemon

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ClaimHeader marks a daemon's intent to launch a stage, posted BEFORE the
// stage's *-started marker. When several daemons watch the same board they can
// decide to launch the same stage at the same instant; each posts a claim, then
// re-reads the thread, and the claim with the lowest server-assigned comment ID
// wins (SC-660 rule 2). Losers back off silently. Like PlanCommentHeader and
// CloseFailedHeader it is content, not a stage transition — it MUST never join
// orderedMarkerSpecs, so ClassifyMarker never sees it and it never moves a card.
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

// winClaim posts this daemon's claim for a stage, re-reads the thread, and
// reports whether this daemon holds the lowest live claim — the winner that may
// proceed to launch. Losers get (false, nil) and back off silently. It is the
// cross-daemon arbiter for rule 2: N daemons that decide to launch the same
// stage at the same instant each post a claim, and exactly one — the lowest
// server comment ID — wins, deterministically and without new infrastructure.
//
// Claim arbitration is keyed on the daemon identity (rule 1). An un-provisioned
// daemon (empty DaemonID) has no identity to contend with, so it skips the claim
// entirely and launches directly — single-daemon behavior, unchanged, mirroring
// StampDaemon's empty-id no-op.
func (d BoardTransitionDeps) winClaim(ctx context.Context, pmKey string, stage BoardStage) (bool, error) {
	if strings.TrimSpace(d.DaemonID) == "" {
		return true, nil
	}
	body := ClaimHeader + "\n" + ClaimStagePrefix + " " + string(stage)
	posted, err := d.Commenter.AddComment(ctx, pmKey, StampDaemon(body, d.DaemonID))
	if err != nil {
		return false, errors.WrapWithDetails(err, "posting stage claim", "pm", pmKey, "stage", string(stage))
	}
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return false, errors.WrapWithDetails(err, "re-reading thread after claim", "pm", pmKey, "stage", string(stage))
	}
	myID := ""
	if posted != nil {
		myID = posted.ID
	}
	return claimWon(comments, stage, myID, d.DaemonID, claimNow()), nil
}

// claimWon reports whether this daemon's claim is the lowest live claim for the
// stage. myClaimID is the server id echoed by AddComment; when a backend does
// not echo one it is recovered from the thread as the newest claim carrying this
// daemon's id. A claim that cannot be identified refuses to win, so a lost id
// never risks a double launch (a later reconcile pass retries).
func claimWon(comments []tracker.Comment, stage BoardStage, myClaimID, myDaemonID string, now time.Time) bool {
	if strings.TrimSpace(myClaimID) == "" {
		myClaimID = newestOwnClaimID(comments, stage, myDaemonID)
	}
	if myClaimID == "" {
		return false
	}
	live := liveClaimIDs(comments, stage, now)
	mineLive := false
	for _, id := range live {
		if id == myClaimID {
			mineLive = true
			break
		}
	}
	if !mineLive {
		return false
	}
	for _, id := range live {
		if id != myClaimID && claimIDLess(id, myClaimID) {
			return false
		}
	}
	return true
}

// liveClaimIDs returns the ids of the stage's claims that are neither fulfilled
// nor expired. A claim is fulfilled once a *-started marker for the stage lands
// at or after it (the launch it represents happened), and expired once ClaimTTL
// elapses with no such marker (the claimant crashed before launching). Only live
// claims contend, so a fulfilled or dead claim never wedges the stage.
func liveClaimIDs(comments []tracker.Comment, stage BoardStage, now time.Time) []string {
	startedComment, hasStarted := latestStartedFor(comments, stage)
	var ids []string
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
		ids = append(ids, c.ID)
	}
	return ids
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
