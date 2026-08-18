package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// IdeaDraftDebounce is how long an idea must sit unchanged before a redraft
// fires. It is a QUIET window, not a rate limit: every observed change pushes
// it out, so an editing session in the tracker's web UI — which bumps
// UpdatedAt on every save — produces one run when the session ends rather than
// one per save. Four board-freshness ticks (30s each): long enough to swallow a
// burst, short enough that the redraft lands inside the idle interval the whole
// feature is built to use.
var IdeaDraftDebounce = 2 * time.Minute

// IdeaDraftRequest is the drafting launch payload. The title travels with it
// only so a log line can name what is being drafted; everything the drafter
// decides, it decides from the ticket itself.
type IdeaDraftRequest struct {
	Key   string `json:"key"`
	Title string `json:"title,omitempty"`
}

// ValidateIdeaDraft rejects a request that names no ticket — a launch with no
// key would start a container that can only fail.
func ValidateIdeaDraft(req IdeaDraftRequest) error {
	if strings.TrimSpace(req.Key) == "" {
		return errors.WithDetails("idea draft request needs a ticket key", "key", req.Key)
	}
	return nil
}

// IdeaDraftWatcher turns the board freshness poll's listing into redrafts.
// It holds only what it has SEEN, never a verdict: whether a redraft may write
// is decided from the ticket and its markers, in the container, every time.
type IdeaDraftWatcher struct {
	Launch   func(IdeaDraftRequest) error
	Debounce time.Duration
	Now      func() time.Time
	Logger   zerolog.Logger

	mu    sync.Mutex
	seen  map[string]time.Time // key -> last observed UpdatedAt
	due   map[string]time.Time // key -> when its debounce window closes
	title map[string]string
}

func (w *IdeaDraftWatcher) debounce() time.Duration {
	if w.Debounce > 0 {
		return w.Debounce
	}
	return IdeaDraftDebounce
}

func (w *IdeaDraftWatcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Observe takes one poll tick's listing and launches a redraft for every idea
// whose debounce window has closed.
//
// The launch happens outside the lock and on its own goroutine: the poll loop
// must never wait on a container start, and a launch failure (no Docker) is a
// debug line rather than a board error — with no substrate the feature degrades
// to no draft, never to an error the user sees.
func (w *IdeaDraftWatcher) Observe(results []TrackerIssuesResult) {
	if w.Launch == nil {
		return
	}
	now := w.now()
	live := map[string]struct{}{}

	w.mu.Lock()
	if w.seen == nil {
		w.seen, w.due, w.title = map[string]time.Time{}, map[string]time.Time{}, map[string]string{}
	}
	for i := range results {
		if results[i].TrackerRole != "pm" || results[i].Err != "" {
			continue
		}
		for j := range results[i].Issues {
			is := results[i].Issues[j]
			if !is.IsIdea() {
				continue
			}
			live[is.Key] = struct{}{}
			w.observeIdea(is.Key, is.Title, is.UpdatedAt, now)
		}
	}
	w.forgetAllBut(live)
	due := w.takeDue(now)
	w.mu.Unlock()

	for _, req := range due {
		go func(req IdeaDraftRequest) {
			if err := w.Launch(req); err != nil {
				w.Logger.Debug().Err(err).Str("key", req.Key).Msg("idea draft: launch failed; no draft this round")
			}
		}(req)
	}
}

// observeIdea records one idea's state, arming the debounce only for a key it
// has seen before: capture fires its own draft, and a daemon restart must not
// redraft every idea on the board. Callers hold the lock.
func (w *IdeaDraftWatcher) observeIdea(key, title string, updated, now time.Time) {
	prev, known := w.seen[key]
	w.seen[key] = updated
	w.title[key] = title
	if known && updated.After(prev) {
		w.due[key] = now.Add(w.debounce())
	}
}

// forgetAllBut drops what is no longer on the board — closed, promoted, or off
// the fetch. A pending redraft for a promoted ticket would fire into a ticket a
// person is editing. Callers hold the lock.
func (w *IdeaDraftWatcher) forgetAllBut(live map[string]struct{}) {
	for key := range w.seen {
		if _, ok := live[key]; !ok {
			delete(w.seen, key)
			delete(w.due, key)
			delete(w.title, key)
		}
	}
}

// takeDue collects and clears every window that has closed. Callers hold the
// lock; the launches themselves happen outside it.
func (w *IdeaDraftWatcher) takeDue(now time.Time) []IdeaDraftRequest {
	var due []IdeaDraftRequest
	for key, at := range w.due {
		if at.After(now) {
			continue
		}
		delete(w.due, key)
		due = append(due, IdeaDraftRequest{Key: key, Title: w.title[key]})
	}
	return due
}
