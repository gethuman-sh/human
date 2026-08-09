package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/tracker"
)

// countingPolicy wraps retryPolicyFor's Attempts so a test can assert the
// retry budget was never even consulted, not just that it stayed at zero.
func countingPolicy(outcome StageExit, recorded bool, relaunched *[]BoardStage, resets *[]BoardStage, calls *int) StageRetry {
	p := retryPolicyFor(outcome, recorded, relaunched, resets)
	inner := p.Attempts
	p.Attempts = func(pmKey string, stage BoardStage) (int, error) {
		*calls++
		return inner(pmKey, stage)
	}
	return p
}

// The SC-2856 incident: a model refusal that kills the agent before it records
// an exit must still be recognised from the hook's own errorType alone — it
// must route to the uncharged paused/outage path, never the generic charged
// failure path.
func TestHandleBoardAgentExit_RateLimitHookRoutesToOutageAndSpendsNoBudget(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	var attemptCalls int
	policy := countingPolicy("", false, &relaunched, &resets, &attemptCalls)

	handleBoardAgentExit(context.Background(), nil, "", "board-SC-1-implementation", "rate_limit", "StopFailure", commenterFor,
		nil, nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Empty(t, relaunched, "the live path never relaunches an outage — reconcile owns the backoff")
	require.Zero(t, attemptCalls, "an unavailability signal must never be charged against the retry budget")

	var sawOutage, sawFailed bool
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok {
			switch state {
			case BoardOutage:
				sawOutage = true
			case BoardFailed:
				sawFailed = true
			}
		}
	}
	require.True(t, sawOutage, "a rate-limit hook signal must post the *-outage marker")
	require.False(t, sawFailed, "and must never red the card with a *-failed marker")
}

// The model-boundary class alone (no hook errorType) must be sufficient to
// route the same way — the refusal killed the agent before ANY hook fired.
func TestHandleBoardAgentExit_RateLimitModelClassRoutesToOutage(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	var attemptCalls int
	policy := countingPolicy("", false, &relaunched, &resets, &attemptCalls)
	latestClass := func(string, string) (string, bool) { return proxy.ClassRateLimit, true }

	handleBoardAgentExit(context.Background(), nil, "", "board-SC-1-implementation", "", "StopFailure", commenterFor,
		nil, nil, nil, nil, alwaysReachable, nil, nil, nil, policy, latestClass, "d1", zerolog.Nop())

	require.Empty(t, relaunched)
	require.Zero(t, attemptCalls)

	var sawOutage, sawFailed bool
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok {
			switch state {
			case BoardOutage:
				sawOutage = true
			case BoardFailed:
				sawFailed = true
			}
		}
	}
	require.True(t, sawOutage, "the model-boundary class alone must suffice to route to outage")
	require.False(t, sawFailed)
}

// An authentication wall does not self-heal: it must red the card (so a person
// sees it) but never charge the retry budget or auto-relaunch — the credential
// will still be broken on the next attempt.
func TestHandleBoardAgentExit_AuthRoutesToUnchargedRed(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	var attemptCalls int
	policy := countingPolicy("", false, &relaunched, &resets, &attemptCalls)

	handleBoardAgentExit(context.Background(), nil, "", "board-SC-1-implementation", "authentication_error", "StopFailure", commenterFor,
		nil, nil, nil, nil, alwaysReachable, nil, nil, nil, policy, nil, "d1", zerolog.Nop())

	require.Empty(t, relaunched, "an auth wall must never be auto-relaunched")
	require.Zero(t, attemptCalls, "and must never charge the retry budget")

	var sawFailed bool
	var failedBody string
	for _, body := range c.added {
		if _, state, ok := ClassifyMarker(body); ok && state == BoardFailed {
			sawFailed = true
			failedBody = body
		}
	}
	require.True(t, sawFailed, "an unrecoverable auth wall must still red the card for a person")
	require.Contains(t, failureReason(failedBody), "re-authenticate", "the reason must name the human action")
}

// classifyUnavailability folds the hook errorType and the model-boundary class
// into one verdict: rate/overload/network are transient and self-heal
// (paused); auth/spend are walls that do not (needs-person); anything else is
// not recognised as unavailability at all.
func TestClassifyUnavailability_table(t *testing.T) {
	always := func(class string) LatestOutcomeClass {
		return func(string, string) (string, bool) { return class, true }
	}
	cases := []struct {
		name      string
		errorType string
		latest    LatestOutcomeClass
		want      endingKind
	}{
		{"hook rate_limit", "rate_limit", nil, endingPaused},
		{"hook overloaded", "overloaded", nil, endingPaused},
		{"hook authentication_error", "authentication_error", nil, endingNeedsPerson},
		{"hook billing", "billing_error", nil, endingNeedsPerson},
		{"class rate-limit", "", always(proxy.ClassRateLimit), endingPaused},
		{"class overload", "", always(proxy.ClassOverload), endingPaused},
		{"class network", "", always(proxy.ClassNetwork), endingPaused},
		{"class auth", "", always(proxy.ClassAuth), endingNeedsPerson},
		{"class spend-limit", "", always(proxy.ClassSpendLimit), endingNeedsPerson},
		{"class ok", "", always(proxy.ClassOK), endingUnknown},
		{"nothing at all", "", nil, endingUnknown},
		{"unrelated errorType", "some_other_error", nil, endingUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _ := classifyUnavailability(tc.errorType, tc.latest, "SC-1", string(BoardImplementation))
			require.Equal(t, tc.want, kind)
		})
	}
}

// parseResumeTime scans a diagnosis for a stated recovery time and returns the
// next absolute instant matching that wall clock, strictly after now.
func TestParseResumeTime(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name string
		text string
		now  time.Time
		want time.Time
		ok   bool
	}{
		{
			name: "am with named zone, later today",
			text: "You've hit your session limit · resets 8:50am (UTC)",
			now:  time.Date(2026, 8, 3, 6, 0, 0, 0, utc),
			want: time.Date(2026, 8, 3, 8, 50, 0, 0, utc),
			ok:   true,
		},
		{
			name: "24h clock, no am/pm",
			text: "resets 22:15",
			now:  time.Date(2026, 8, 3, 6, 0, 0, 0, utc),
			want: time.Date(2026, 8, 3, 22, 15, 0, 0, utc),
			ok:   true,
		},
		{
			name: "already past today rolls to tomorrow",
			text: "resets 3:00am (UTC)",
			now:  time.Date(2026, 8, 3, 6, 0, 0, 0, utc),
			want: time.Date(2026, 8, 4, 3, 0, 0, 0, utc),
			ok:   true,
		},
		{
			name: "garbage has no match",
			text: "the agent crashed for no reason we can name",
			now:  time.Date(2026, 8, 3, 6, 0, 0, 0, utc),
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseResumeTime(tc.text, tc.now, utc)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.True(t, got.Equal(tc.want), "got %s want %s", got, tc.want)
			}
		})
	}
}
