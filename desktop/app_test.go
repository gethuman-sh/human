//go:build wailsapp

package main

import (
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/appearance"
	"github.com/gethuman-sh/human/internal/boardprefs"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// noDeclaredIdentity is the unconfigured project: .humanconfig declares no "me"
// names, so the viewer falls back to asking the tracker.
func noDeclaredIdentity(string) (vieweridentity.Identity, error) {
	return vieweridentity.Identity{}, nil
}

// noDeclaredAppearance is the unconfigured project: .humanconfig declares no
// "ui" section, so the board keeps the stylesheet's shipped dimming.
func noDeclaredAppearance(string) (appearance.Appearance, error) {
	return appearance.Appearance{}, nil
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

// SC-3409: a declared dimming strength reaches the board unchanged, so editing
// .humanconfig and refreshing shows the new value with no rebuild.
func TestBoardAppearanceUsesDeclaredPercent(t *testing.T) {
	app := &App{appearanceConfig: func(dir string) (appearance.Appearance, error) {
		return appearance.Appearance{Dim: 20}, nil
	}}
	info := daemon.DaemonInfo{
		Addr: "127.0.0.1:0", Token: "t",
		Projects: []daemon.ProjectInfo{{Dir: "/tmp/project"}},
	}

	if got := app.boardAppearance(info); got != 20 {
		t.Fatalf("boardAppearance = %d, want 20", got)
	}
}

// Without a project directory there is no config to read, so the loader must
// not even be asked — there is nothing it could be asked about.
func TestBoardAppearanceWithoutProjectDirSaysNothing(t *testing.T) {
	calls := 0
	app := &App{appearanceConfig: func(dir string) (appearance.Appearance, error) {
		calls++
		return noDeclaredAppearance(dir)
	}}

	if got := app.boardAppearance(daemon.DaemonInfo{}); got != 0 {
		t.Fatalf("boardAppearance = %d, want 0", got)
	}
	if calls != 0 {
		t.Fatalf("no project dir must not consult the config, got %d reads", calls)
	}
}

// A malformed config must degrade to the shipped default, never to a blank
// board or a crash: the person can still read every card while they fix it.
func TestBoardAppearanceFallsBackOnUnreadableConfig(t *testing.T) {
	app := &App{appearanceConfig: func(dir string) (appearance.Appearance, error) {
		return appearance.Appearance{Dim: 20}, errors.WithDetails("malformed config")
	}}
	info := daemon.DaemonInfo{Projects: []daemon.ProjectInfo{{Dir: "/tmp/project"}}}

	if got := app.boardAppearance(info); got != 0 {
		t.Fatalf("boardAppearance = %d, want 0 on a failed read", got)
	}
}

// Out of range is rejected rather than clamped, so a typo lands on the shipped
// default instead of on an almost-invisible board.
func TestBoardAppearanceRejectsOutOfRange(t *testing.T) {
	info := daemon.DaemonInfo{Projects: []daemon.ProjectInfo{{Dir: "/tmp/project"}}}
	for _, dim := range []int{0, 500} {
		app := &App{appearanceConfig: func(dir string) (appearance.Appearance, error) {
			return appearance.Appearance{Dim: dim}, nil
		}}
		if got := app.boardAppearance(info); got != 0 {
			t.Fatalf("boardAppearance for Dim=%d = %d, want 0", dim, got)
		}
	}
}

// The strength rides the same viewer-local overlay as the ownership flag it
// modulates, so both halves of the feature land on the payload together.
func TestApplyLocalCarriesDimPercent(t *testing.T) {
	got := applyLocal(daemon.BoardView{}, nil, nil, boardprefs.Prefs{}, vieweridentity.Identity{}, 20)
	if got.DimPercent != 20 {
		t.Fatalf("DimPercent = %d, want 20", got.DimPercent)
	}

	// Zero stays zero so it is omitted from the wire and the frontend leaves
	// the stylesheet alone.
	got = applyLocal(daemon.BoardView{}, nil, nil, boardprefs.Prefs{}, vieweridentity.Identity{}, 0)
	if got.DimPercent != 0 {
		t.Fatalf("DimPercent = %d, want 0", got.DimPercent)
	}
}
