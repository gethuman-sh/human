package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// resultsFor wraps issues as one tracker result, the shape the poll hands to
// the fingerprint and to an observer.
func resultsFor(issues ...tracker.Issue) []TrackerIssuesResult {
	return []TrackerIssuesResult{{Project: "p", Issues: issues}}
}

// listerFor returns an IssueLister yielding one tracker result carrying issues.
func listerFor(issues ...tracker.Issue) IssueLister {
	return func() ([]TrackerIssuesResult, error) { return resultsFor(issues...), nil }
}

func TestFingerprintIssues_StableAndOrderIndependent(t *testing.T) {
	a := tracker.Issue{Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(100, 0)}
	b := tracker.Issue{Key: "SC-2", Title: "two", Status: "Done", UpdatedAt: time.Unix(200, 0)}

	fp1 := fingerprintIssues(resultsFor(a, b))
	// Same set, reversed order → identical digest.
	fp2 := fingerprintIssues(resultsFor(b, a))
	if fp1 != fp2 {
		t.Fatalf("digest is order-dependent: %s != %s", fp1, fp2)
	}
}

func TestFingerprintIssues_DetectsChanges(t *testing.T) {
	base := tracker.Issue{Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(100, 0)}
	fpBase := fingerprintIssues(resultsFor(base))

	cases := map[string]tracker.Issue{
		"new title":  {Key: "SC-1", Title: "renamed", Status: "To Do", UpdatedAt: time.Unix(100, 0)},
		"new status": {Key: "SC-1", Title: "one", Status: "In Progress", UpdatedAt: time.Unix(100, 0)},
		"new mtime":  {Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(101, 0)},
	}
	for name, edited := range cases {
		t.Run(name, func(t *testing.T) {
			fp := fingerprintIssues(resultsFor(edited))
			if fp == fpBase {
				t.Fatalf("edit %q not reflected in digest", name)
			}
		})
	}

	// A new ticket joining the set changes the digest.
	fpAdded := fingerprintIssues(resultsFor(base, tracker.Issue{Key: "SC-2", Title: "two"}))
	if fpAdded == fpBase {
		t.Fatal("added ticket not reflected in digest")
	}
}

func TestFreshnessStep_BaselineThenChange(t *testing.T) {
	var st freshnessState

	if st.step("A") {
		t.Fatal("first watched poll must only baseline, not poke")
	}
	if st.step("A") {
		t.Fatal("unchanged fingerprint must not poke")
	}
	if !st.step("B") {
		t.Fatal("changed fingerprint must poke")
	}
}

func TestRunBoardFreshnessPoll_NilArgsReturn(t *testing.T) {
	// Must not panic or block; a nil dependency disables the loop.
	RunBoardFreshnessPoll(context.Background(), BoardFreshnessOpts{
		Poke: func() {}, HasWatchers: func() bool { return true },
		Interval: time.Millisecond, Logger: zerolog.Nop(),
	})
}

// The three branches that moved out of step and into the loop, tested where
// they now live: no watchers never lists and drops the baseline, a listing
// error neither pokes nor reaches the observer, and the observer sees exactly
// the listing the fingerprint was taken from.
func TestRunBoardFreshnessPoll_NoWatchersNeitherListsNorPokes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var listed atomic.Int32
	poked := make(chan struct{}, 1)
	oldJitter := BoardFreshnessJitter
	BoardFreshnessJitter = 0
	defer func() { BoardFreshnessJitter = oldJitter }()

	go RunBoardFreshnessPoll(ctx, BoardFreshnessOpts{
		List: func() ([]TrackerIssuesResult, error) {
			listed.Add(1)
			return resultsFor(tracker.Issue{Key: "SC-1"}), nil
		},
		Poke:        func() { poked <- struct{}{} },
		HasWatchers: func() bool { return false },
		Interval:    2 * time.Millisecond,
		Logger:      zerolog.Nop(),
	})

	time.Sleep(20 * time.Millisecond)
	if listed.Load() != 0 {
		t.Fatalf("must not list the tracker with no board open, listed %d times", listed.Load())
	}
	select {
	case <-poked:
		t.Fatal("must not poke with no watchers")
	default:
	}
}

func TestRunBoardFreshnessPoll_ListErrorNeitherPokesNorObserves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	boom := errors.New("tracker down")
	var observed, calls atomic.Int32
	poked := make(chan struct{}, 1)
	oldJitter := BoardFreshnessJitter
	BoardFreshnessJitter = 0
	defer func() { BoardFreshnessJitter = oldJitter }()

	go RunBoardFreshnessPoll(ctx, BoardFreshnessOpts{
		List: func() ([]TrackerIssuesResult, error) {
			calls.Add(1)
			return nil, boom
		},
		Poke:        func() { poked <- struct{}{} },
		HasWatchers: func() bool { return true },
		Observe:     func([]TrackerIssuesResult) { observed.Add(1) },
		Interval:    2 * time.Millisecond,
		Logger:      zerolog.Nop(),
	})

	time.Sleep(20 * time.Millisecond)
	if calls.Load() < 2 {
		t.Fatalf("the loop must keep retrying after a listing error, listed %d times", calls.Load())
	}
	if observed.Load() != 0 {
		t.Fatal("an observer must never see a failed listing")
	}
	select {
	case <-poked:
		t.Fatal("a list error must not poke")
	default:
	}
}

func TestRunBoardFreshnessPoll_ObserverSeesTheSameListing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seen := make(chan []TrackerIssuesResult, 4)
	oldJitter := BoardFreshnessJitter
	BoardFreshnessJitter = 0
	defer func() { BoardFreshnessJitter = oldJitter }()

	go RunBoardFreshnessPoll(ctx, BoardFreshnessOpts{
		List:        listerFor(tracker.Issue{Key: "SC-1", Title: "one"}),
		Poke:        func() {},
		HasWatchers: func() bool { return true },
		Observe: func(results []TrackerIssuesResult) {
			select {
			case seen <- results:
			default:
			}
		},
		Interval: 2 * time.Millisecond,
		Logger:   zerolog.Nop(),
	})

	select {
	case results := <-seen:
		if len(results) != 1 || len(results[0].Issues) != 1 || results[0].Issues[0].Key != "SC-1" {
			t.Fatalf("observer got a different listing than the poll fetched: %+v", results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the observer must be handed the listing the poll already paid for")
	}
}

func TestRunBoardFreshnessPoll_PokesOnChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Toggle the ticket set after the baseline tick so the loop observes a change.
	changed := make(chan struct{})
	list := func() ([]TrackerIssuesResult, error) {
		select {
		case <-changed:
			return []TrackerIssuesResult{{Issues: []tracker.Issue{{Key: "SC-2"}}}}, nil
		default:
			return []TrackerIssuesResult{{Issues: []tracker.Issue{{Key: "SC-1"}}}}, nil
		}
	}
	poked := make(chan struct{}, 1)
	poke := func() {
		select {
		case poked <- struct{}{}:
		default:
		}
	}

	oldJitter := BoardFreshnessJitter
	BoardFreshnessJitter = 0
	defer func() { BoardFreshnessJitter = oldJitter }()

	go RunBoardFreshnessPoll(ctx, BoardFreshnessOpts{
		List: list, Poke: poke, HasWatchers: func() bool { return true },
		Interval: 2 * time.Millisecond, Logger: zerolog.Nop(),
	})

	time.Sleep(10 * time.Millisecond) // let the baseline settle
	close(changed)

	select {
	case <-poked:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a poke after the ticket set changed")
	}
}
