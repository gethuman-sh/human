//go:build wailsapp

package main

import (
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// SC-3339: a transient failure fetching the viewer's identity (locked vault
// prompt, credential blip, daemon still on an older protocol) must not latch
// the board into "no dimming" for the rest of the app's lifetime — only a
// *successful* fetch may be memoized, so the next board refresh retries.
func TestViewerNameRetriesAfterFailure(t *testing.T) {
	calls := 0
	app := &App{
		currentUserFetch: func(addr, token string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.WithDetails("transient credential blip")
			}
			return "alice", nil
		},
	}
	info := daemon.DaemonInfo{Addr: "127.0.0.1:0", Token: "t"}

	if got := app.viewerName(info); got != "" {
		t.Fatalf("first call: got %q, want empty on failure", got)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch after the first call, got %d", calls)
	}

	if got := app.viewerName(info); got != "alice" {
		t.Fatalf("second call: got %q, want %q — a failed fetch must not block a retry", got, "alice")
	}
	if calls != 2 {
		t.Fatalf("expected the second call to retry the fetch, got %d total calls", calls)
	}

	// A resolved name must stay memoized: no further IPC calls.
	if got := app.viewerName(info); got != "alice" {
		t.Fatalf("third call: got %q, want %q from cache", got, "alice")
	}
	if calls != 2 {
		t.Fatalf("expected the resolved name to be memoized (no extra fetch), got %d total calls", calls)
	}
}
