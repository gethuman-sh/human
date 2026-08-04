//go:build wailsapp

package main

import (
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// noDeclaredIdentity is the unconfigured project: .humanconfig declares no "me"
// names, so the viewer falls back to asking the tracker.
func noDeclaredIdentity(string) (vieweridentity.Identity, error) {
	return vieweridentity.Identity{}, nil
}

// SC-3339: a transient failure fetching the viewer's identity (locked vault
// prompt, credential blip, daemon still on an older protocol) must not latch
// the board into "no dimming" for the rest of the app's lifetime — only a
// *successful* fetch may be memoized, so the next board refresh retries.
func TestViewerIdentityRetriesAfterFailure(t *testing.T) {
	calls := 0
	app := &App{
		viewerConfig: noDeclaredIdentity,
		currentUserFetch: func(addr, token string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.WithDetails("transient credential blip")
			}
			return "alice", nil
		},
	}
	info := daemon.DaemonInfo{Addr: "127.0.0.1:0", Token: "t"}

	if got := app.viewerIdentity(info); got.Known() {
		t.Fatalf("first call: got %v, want an unknown viewer on failure", got.Names)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch after the first call, got %d", calls)
	}

	if got := app.viewerIdentity(info); !got.Matches("alice") {
		t.Fatalf("second call: got %v, want alice — a failed fetch must not block a retry", got.Names)
	}
	if calls != 2 {
		t.Fatalf("expected the second call to retry the fetch, got %d total calls", calls)
	}

	// A resolved name must stay memoized: no further IPC calls.
	if got := app.viewerIdentity(info); !got.Matches("alice") {
		t.Fatalf("third call: got %v, want alice from cache", got.Names)
	}
	if calls != 2 {
		t.Fatalf("expected the resolved name to be memoized (no extra fetch), got %d total calls", calls)
	}
}

// The declared identity is authoritative: it covers every tracker at once and
// cannot fail into "everything looks mine", so a project that declares one must
// never be asked to spend a credential and a round trip on the question.
func TestViewerIdentityPrefersDeclaredNames(t *testing.T) {
	calls := 0
	app := &App{
		viewerConfig: func(dir string) (vieweridentity.Identity, error) {
			return vieweridentity.Identity{Names: []string{"Alice", "alice-gh"}}, nil
		},
		currentUserFetch: func(addr, token string) (string, error) {
			calls++
			return "someone-else", nil
		},
	}
	info := daemon.DaemonInfo{
		Addr: "127.0.0.1:0", Token: "t",
		Projects: []daemon.ProjectInfo{{Dir: "/tmp/project"}},
	}

	got := app.viewerIdentity(info)

	if !got.Matches("alice-gh") {
		t.Fatalf("declared identity must be used: got %v", got.Names)
	}
	if calls != 0 {
		t.Fatalf("a declared identity must not ask the tracker, got %d fetches", calls)
	}
}
