package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// echoCommenter is a commenter that reads back what it was told — the tracker
// behaviour the say-once guard depends on, which syncCommenter deliberately
// does not model.
type echoCommenter struct {
	mu       sync.Mutex
	comments []tracker.Comment
	added    []string
}

func (e *echoCommenter) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]tracker.Comment, len(e.comments))
	copy(out, e.comments)
	return out, nil
}

func (e *echoCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := tracker.Comment{Body: body, Created: time.Now().Add(time.Duration(len(e.comments)) * time.Second)}
	e.added = append(e.added, body)
	e.comments = append(e.comments, c)
	return &c, nil
}

// Every relaunched agent that re-hits the same outage exits the same way. The
// first exit states it; the second must not repeat it (SC-2851 — this is the
// comment flood, one marker per attempt for as long as the substrate is down).
func TestHandleBoardAgentExit_OutageIsStatedOnce(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &echoCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitOutage, true, &relaunched, &resets)

	for range 3 {
		handleBoardAgentExit(context.Background(), nil, "", "board-SC-1-implementation", "", "", commenterFor,
			nil, nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())
	}

	require.Len(t, c.added, 1, "the card says the substrate is down once, not once per attempt")
	require.Contains(t, c.added[0], ImplementationOutageHeader)
}

// The bug (SC-2851): an outage that never ends waited forever. Drive an outage
// card past OutageWaitBound and it must stop relaunching, red for a human, name
// what was unreachable and for how long — and still charge nothing.
func TestReconcileOutage_HandsAnUnendingWaitToAPerson(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-OutageWaitBound-2*time.Hour)),
			cmt(ImplementationOutageHeader+"\n1Password is not reachable", now.Add(-OutageWaitBound-time.Hour)),
		},
	}}
	var relaunched []BoardStage
	attempts := 0
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}
	var posted []struct{ Key, Body string }

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), liveAgents(),
		capturingPoster(&posted), retry, "d1", now, zerolog.Nop())

	require.Zero(t, redriven, "a wait past the bound is not relaunched again")
	require.Equal(t, 1, handedOver)
	require.Empty(t, relaunched)
	require.Zero(t, attempts, "ending the wait must still never charge the retry budget")
	require.Len(t, posted, 1)
	require.Equal(t, "SC-1", posted[0].Key)
	require.Contains(t, posted[0].Body, ImplementationFailedHeader, "the card must red so a person sees it")
	require.Contains(t, posted[0].Body, "1Password is not reachable", "it names what could not be reached")
	require.Contains(t, posted[0].Body, "7h0m0s", "and for how long it has been waiting")

	// The badge reads the marker's first body line: it must say a person is needed.
	require.Contains(t, failureReason(posted[0].Body), "needs a person")
}

// Once handed over, the card derives failed, so the next tick skips it — the
// handover states itself once, exactly like the outage marker it replaces.
func TestReconcileOutage_HandoverIsIdempotent(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationOutageHeader+"\nno tracker", now.Add(-OutageWaitBound-time.Hour)),
			cmt(ImplementationFailedHeader+"\nwaited 7h0m0s for the substrate to come back", now.Add(-time.Minute)),
		},
	}}
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(string, BoardStage) (bool, error) { return true, nil },
	}
	var posted []struct{ Key, Body string }

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), liveAgents(),
		capturingPoster(&posted), retry, "d1", now, zerolog.Nop())

	require.Zero(t, redriven)
	require.Zero(t, handedOver)
	require.Empty(t, posted, "the card is no longer in outage: nothing to say again")
}

// An unwired poster must not strand the card with neither a relaunch nor a red:
// without a way to tell a person, the indefinite wait is still the better half.
func TestReconcileOutage_WithoutAPosterKeepsWaiting(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt(ImplementationOutageHeader+"\nno tracker", now.Add(-OutageWaitBound-time.Hour))},
	}}
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), liveAgents(),
		nil, retry, "d1", now, zerolog.Nop())

	require.Equal(t, 1, redriven)
	require.Zero(t, handedOver)
	require.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

// The wait is timed from the START of the current outage run, not from its
// newest marker — otherwise a re-posted marker would reset the clock forever
// and the bound could never be reached.
func TestOutageRunSince_AnchorsOnTheOldestMarkerOfTheRun(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	comments := []tracker.Comment{
		cmt(ImplementationStartedHeader, now.Add(-10*time.Hour)),
		cmt(ImplementationOutageHeader+"\nno vault", now.Add(-9*time.Hour)),
		cmt(ImplementationOutageHeader+"\nno vault", now.Add(-8*time.Hour)),
		cmt(ImplementationOutageHeader+"\nno vault", now.Add(-time.Hour)),
	}

	since, ok := outageRunSince(comments, BoardImplementation)

	require.True(t, ok)
	require.Equal(t, now.Add(-9*time.Hour), since)
}

// A substrate that came back and went down again is timed from the SECOND
// outage: the started marker in between ends the first run.
func TestOutageRunSince_ResetsAfterTheSubstrateCameBack(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	comments := []tracker.Comment{
		cmt(ImplementationOutageHeader+"\nno vault", now.Add(-9*time.Hour)),
		cmt(ImplementationStartedHeader, now.Add(-5*time.Hour)),
		cmt(ImplementationOutageHeader+"\nno vault", now.Add(-2*time.Hour)),
	}

	since, ok := outageRunSince(comments, BoardImplementation)

	require.True(t, ok)
	require.Equal(t, now.Add(-2*time.Hour), since)
}

// Another stage's outage is not this stage's wait.
func TestOutageRunSince_IgnoresOtherStages(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	comments := []tracker.Comment{cmt(PlanningOutageHeader+"\nno vault", now.Add(-9*time.Hour))}

	_, ok := outageRunSince(comments, BoardImplementation)

	require.False(t, ok)
}

// Say it once: an identical outage statement already standing as the stage's
// newest marker is not posted again (the weekend-long comment flood).
func TestOutageAlreadyStated(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	body := ImplementationOutageHeader + "\nno vault"
	comments := []tracker.Comment{cmt(marker.Sign(body, "d1", "rev1"), now.Add(-time.Hour))}

	require.True(t, outageAlreadyStated(comments, BoardImplementation, body),
		"the same statement, even signed by another machine, is a repeat")
	require.True(t, outageAlreadyStated(comments, BoardImplementation, marker.Sign(body, "d2", "rev2")),
		"two machines on different builds saying the same thing dedup")
	require.False(t, outageAlreadyStated(comments, BoardImplementation, ImplementationOutageHeader+"\nno tracker"),
		"a different substrate is different news and must be posted")
	require.False(t, outageAlreadyStated(comments, BoardVerification, ReviewOutageHeader+"\nno vault"),
		"another stage has said nothing yet")
}

// A card whose newest marker is not an outage has nothing standing to repeat.
func TestOutageAlreadyStated_NotWhileTheStageMovedOn(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	body := ImplementationOutageHeader + "\nno vault"
	comments := []tracker.Comment{
		cmt(body, now.Add(-2*time.Hour)),
		cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
	}

	require.False(t, outageAlreadyStated(comments, BoardImplementation, body))
}
