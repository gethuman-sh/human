package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// listerFor returns an IssueLister yielding one tracker result carrying issues.
func listerFor(issues ...tracker.Issue) IssueLister {
	return func() ([]TrackerIssuesResult, error) {
		return []TrackerIssuesResult{{Project: "p", Issues: issues}}, nil
	}
}

func TestFingerprintIssues_StableAndOrderIndependent(t *testing.T) {
	a := tracker.Issue{Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(100, 0)}
	b := tracker.Issue{Key: "SC-2", Title: "two", Status: "Done", UpdatedAt: time.Unix(200, 0)}

	fp1, err := fingerprintIssues(listerFor(a, b))
	if err != nil {
		t.Fatal(err)
	}
	// Same set, reversed order → identical digest.
	fp2, err := fingerprintIssues(listerFor(b, a))
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("digest is order-dependent: %s != %s", fp1, fp2)
	}
}

func TestFingerprintIssues_DetectsChanges(t *testing.T) {
	base := tracker.Issue{Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(100, 0)}
	fpBase, _ := fingerprintIssues(listerFor(base))

	cases := map[string]tracker.Issue{
		"new title":  {Key: "SC-1", Title: "renamed", Status: "To Do", UpdatedAt: time.Unix(100, 0)},
		"new status": {Key: "SC-1", Title: "one", Status: "In Progress", UpdatedAt: time.Unix(100, 0)},
		"new mtime":  {Key: "SC-1", Title: "one", Status: "To Do", UpdatedAt: time.Unix(101, 0)},
	}
	for name, edited := range cases {
		t.Run(name, func(t *testing.T) {
			fp, _ := fingerprintIssues(listerFor(edited))
			if fp == fpBase {
				t.Fatalf("edit %q not reflected in digest", name)
			}
		})
	}

	// A new ticket joining the set changes the digest.
	fpAdded, _ := fingerprintIssues(listerFor(base, tracker.Issue{Key: "SC-2", Title: "two"}))
	if fpAdded == fpBase {
		t.Fatal("added ticket not reflected in digest")
	}
}

func TestFingerprintIssues_PropagatesError(t *testing.T) {
	boom := errors.New("tracker down")
	if _, err := fingerprintIssues(func() ([]TrackerIssuesResult, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("want propagated error, got %v", err)
	}
}

// fp is a fingerprint stub returning canned values so step's decision logic is
// tested without any real listing or timers.
func fpSeq(values ...string) func() (string, error) {
	i := 0
	return func() (string, error) {
		v := values[i]
		if i < len(values)-1 {
			i++
		}
		return v, nil
	}
}

func TestFreshnessStep_BaselineThenChange(t *testing.T) {
	var st freshnessState
	fp := fpSeq("A", "A", "B")
	log := zerolog.Nop()

	if st.step(true, fp, log) {
		t.Fatal("first watched poll must only baseline, not poke")
	}
	if st.step(true, fp, log) {
		t.Fatal("unchanged fingerprint must not poke")
	}
	if !st.step(true, fp, log) {
		t.Fatal("changed fingerprint must poke")
	}
}

func TestFreshnessStep_NoWatchersNeverPokesAndResets(t *testing.T) {
	var st freshnessState
	log := zerolog.Nop()

	// Establish a baseline while watched.
	st.step(true, fpSeq("A"), log)
	// Watchers gone: never poke, and the baseline is dropped.
	if st.step(false, fpSeq("B"), log) {
		t.Fatal("must not poke with no watchers")
	}
	if st.haveBaseline {
		t.Fatal("baseline must reset when watchers drop")
	}
	// Watcher returns mid-change: re-baselines silently (the UI's reconnect
	// fetch covers the gap), so the first poll back does not poke.
	if st.step(true, fpSeq("C"), log) {
		t.Fatal("first poll after watchers return must re-baseline, not poke")
	}
}

func TestFreshnessStep_ListErrorDoesNotPoke(t *testing.T) {
	var st freshnessState
	log := zerolog.Nop()
	st.step(true, fpSeq("A"), log) // baseline
	errFn := func() (string, error) { return "", errors.New("boom") }
	if st.step(true, errFn, log) {
		t.Fatal("a list error must not poke")
	}
}

func TestRunBoardFreshnessPoll_NilArgsReturn(t *testing.T) {
	// Must not panic or block; a nil dependency disables the loop.
	RunBoardFreshnessPoll(context.Background(), nil, func() {}, func() bool { return true }, time.Millisecond, zerolog.Nop())
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

	go RunBoardFreshnessPoll(ctx, list, poke, func() bool { return true }, 2*time.Millisecond, zerolog.Nop())

	time.Sleep(10 * time.Millisecond) // let the baseline settle
	close(changed)

	select {
	case <-poked:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a poke after the ticket set changed")
	}
}
