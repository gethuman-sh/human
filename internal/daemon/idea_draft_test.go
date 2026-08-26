package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// launchRecorder collects launches; the watcher fires them on their own
// goroutines, so reads are locked and waited for.
type launchRecorder struct {
	mu   sync.Mutex
	reqs []IdeaDraftRequest
}

func (r *launchRecorder) launch(req IdeaDraftRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return nil
}

func (r *launchRecorder) all() []IdeaDraftRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]IdeaDraftRequest(nil), r.reqs...)
}

// settle waits for the watcher's launch goroutines to arrive at a count that
// stops changing, so an assertion of "exactly one" cannot pass by racing.
func (r *launchRecorder) settle() []IdeaDraftRequest {
	for range 50 {
		before := len(r.all())
		time.Sleep(2 * time.Millisecond)
		if len(r.all()) == before {
			break
		}
	}
	return r.all()
}

func ideaResults(issues ...tracker.Issue) []TrackerIssuesResult {
	return []TrackerIssuesResult{{TrackerRole: "pm", Issues: issues}}
}

func idea(key, title string, updated time.Time) tracker.Issue {
	return tracker.Issue{Key: key, Title: title, Labels: []string{"human/idea"}, UpdatedAt: updated}
}

func watcherAt(now *time.Time, rec *launchRecorder) *IdeaDraftWatcher {
	return &IdeaDraftWatcher{
		Launch:   rec.launch,
		Debounce: time.Minute,
		Now:      func() time.Time { return *now },
		Logger:   zerolog.Nop(),
	}
}

// A key the watcher has never seen only baselines: capture fires its own draft,
// and a daemon restart must not redraft every idea on the board.
func TestIdeaDraftWatcher_BaselinesWithoutLaunching(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	assert.Empty(t, rec.settle())
}

func TestIdeaDraftWatcher_DebouncesABurst(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	for i := range 3 {
		now = now.Add(10 * time.Second)
		w.Observe(ideaResults(idea("SC-1", fmt.Sprintf("edit %d", i), time.Unix(int64(600+i*10), 0))))
	}
	require.Empty(t, rec.all(), "a burst inside the window fires nothing yet")

	// The editing stops: the next tick sees the same title, so the window
	// closes instead of being pushed out again.
	now = now.Add(2 * time.Minute)
	w.Observe(ideaResults(idea("SC-1", "edit 2", time.Unix(620, 0))))

	launched := rec.settle()
	require.Len(t, launched, 1, "a burst produces exactly one run")
	assert.Equal(t, "SC-1", launched[0].Key)
	assert.Equal(t, "edit 2", launched[0].Title, "the launch carries the last-seen title")
}

// The loop-breaker. The drafter's own write advances UpdatedAt without touching
// the title, so arming on the advance alone would relaunch the run that write
// came from, once per quiet window, for as long as a board stays open.
func TestIdeaDraftWatcher_IgnoresADescriptionOnlyChange(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	for i := range 4 {
		now = now.Add(5 * time.Minute)
		w.Observe(ideaResults(idea("SC-1", "one", time.Unix(int64(600+i*10), 0))))
	}

	assert.Empty(t, rec.settle(), "an UpdatedAt advance the title did not move is not a redraft trigger")
	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(t, w.due, "nothing was armed, so nothing stays pending")
}

// The title moving after a run of description-only advances still arms: the
// filter is the title, not a one-shot latch on the first change seen.
func TestIdeaDraftWatcher_ArmsOnATitleChangeAfterQuietAdvances(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	now = now.Add(5 * time.Minute)
	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(600, 0)))) // the drafter's own write
	now = now.Add(5 * time.Minute)
	w.Observe(ideaResults(idea("SC-1", "one better idea", time.Unix(700, 0))))
	require.Empty(t, rec.all(), "the window has just opened")

	now = now.Add(5 * time.Minute)
	w.Observe(ideaResults(idea("SC-1", "one better idea", time.Unix(700, 0))))

	launched := rec.settle()
	require.Len(t, launched, 1)
	assert.Equal(t, "one better idea", launched[0].Title)
}

func TestIdeaDraftWatcher_IgnoresNonIdeaAndNonPM(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	feature := tracker.Issue{Key: "SC-9", Title: "work", UpdatedAt: time.Unix(500, 0)}
	engineering := []TrackerIssuesResult{{TrackerRole: "engineering", Issues: []tracker.Issue{idea("HUM-1", "eng idea", time.Unix(500, 0))}}}

	w.Observe(append(ideaResults(feature), engineering...))
	// Both titles move, so the label and the role are the only things that can
	// hold the launches back.
	now = now.Add(5 * time.Minute)
	w.Observe(append(ideaResults(tracker.Issue{Key: "SC-9", Title: "work renamed", UpdatedAt: time.Unix(900, 0)}),
		[]TrackerIssuesResult{{TrackerRole: "engineering", Issues: []tracker.Issue{idea("HUM-1", "eng idea renamed", time.Unix(900, 0))}}}...))

	assert.Empty(t, rec.settle())
}

// Promotion removes the idea labels while a window may still be armed; the key
// leaves the listing and the pending redraft goes with it.
func TestIdeaDraftWatcher_ForgetsAPromotedKey(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	now = now.Add(time.Second)
	w.Observe(ideaResults(idea("SC-1", "one renamed", time.Unix(600, 0))))

	now = now.Add(5 * time.Minute)
	w.Observe(ideaResults()) // promoted: no longer an idea, no longer listed

	assert.Empty(t, rec.settle())
	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(t, w.seen)
	assert.Empty(t, w.due)
	assert.Empty(t, w.title)
}

func TestIdeaDraftWatcher_SkipsAFailedTrackerResult(t *testing.T) {
	rec := &launchRecorder{}
	now := time.Unix(1000, 0)
	w := watcherAt(&now, rec)

	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
	now = now.Add(5 * time.Minute)
	w.Observe([]TrackerIssuesResult{{TrackerRole: "pm", Err: "tracker unreachable"}})

	assert.Empty(t, rec.settle(), "a failed listing is not evidence that anything changed")
}

func TestIdeaDraftWatcher_NilLauncherIsInert(t *testing.T) {
	w := &IdeaDraftWatcher{Logger: zerolog.Nop()}
	w.Observe(ideaResults(idea("SC-1", "one", time.Unix(500, 0))))
}

func TestValidateIdeaDraft(t *testing.T) {
	require.Error(t, ValidateIdeaDraft(IdeaDraftRequest{}))
	require.Error(t, ValidateIdeaDraft(IdeaDraftRequest{Key: "   "}))
	assert.NoError(t, ValidateIdeaDraft(IdeaDraftRequest{Key: "SC-1"}))
}
