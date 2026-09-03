package daemon

import (
	"context"
	"sync"

	"github.com/gethuman-sh/human/internal/tracker"
)

// fakeRunner returns queued turns/errors in order, recording each call.
type fakeRunner struct {
	mu    sync.Mutex
	turns []ChatTurn
	errs  []error
	calls []struct{ resumeID, prompt string }
}

func (f *fakeRunner) Run(_ context.Context, resumeID, prompt string) (ChatTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ resumeID, prompt string }{resumeID, prompt})
	idx := len(f.calls) - 1
	var turn ChatTurn
	var err error
	if idx < len(f.turns) {
		turn = f.turns[idx]
	}
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	return turn, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) callAt(i int) struct{ resumeID, prompt string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// fakeCreator is a function-backed tracker.Creator capturing the issue.
type fakeCreator struct {
	mu       sync.Mutex
	captured *tracker.Issue
	created  *tracker.Issue
	err      error
}

func (f *fakeCreator) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = issue
	if f.err != nil {
		return nil, f.err
	}
	return f.created, nil
}

func (f *fakeCreator) capturedIssue() *tracker.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captured
}

func newFakeCreator() *fakeCreator {
	return &fakeCreator{created: &tracker.Issue{Key: "SC-999", URL: "https://app.shortcut.com/x/story/999"}}
}

// fakeEditor is a function-backed tracker.Editor capturing the edit.
type fakeEditor struct {
	mu       sync.Mutex
	key      string
	opts     tracker.EditOptions
	returned *tracker.Issue
	err      error
}

func (f *fakeEditor) EditIssue(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key = key
	f.opts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.returned, nil
}

func (f *fakeEditor) captured() (string, tracker.EditOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.key, f.opts
}
