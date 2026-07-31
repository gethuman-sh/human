package tracker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
)

// stubForge is a minimal forge.Forge for exercising the tracker/forge split.
type stubForge struct{}

func (stubForge) CreatePullRequest(context.Context, *forge.PullRequest) (*forge.PullRequest, error) {
	return nil, nil
}

// forgeOnly is a forge-only instance as built from role: forge / a forges: entry:
// a Forge client with no tracker Provider.
func forgeOnly(name string) Instance {
	return Instance{Name: name, Kind: "github", Role: RoleForge, Forge: stubForge{}}
}

func TestIsTracker(t *testing.T) {
	assert.True(t, Instance{Kind: "github", Provider: stubProvider{}}.IsTracker())
	assert.False(t, forgeOnly("prs").IsTracker())
}

func TestTrackerInstances_dropsForgeOnly(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "linear", Provider: stubProvider{}},
		forgeOnly("prs"),
	}
	got := TrackerInstances(instances)
	require.Len(t, got, 1)
	assert.Equal(t, "work", got[0].Name)
}

// TestResolveAutoDetect_ignoresForgeOnly is the core SC-1671 repro: a single real
// tracker beside a forge-only GitHub entry must resolve without --tracker.
func TestResolveAutoDetect_ignoresForgeOnly(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "linear", Provider: stubProvider{}},
		forgeOnly("prs"),
	}
	inst, err := Resolve("", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "work", inst.Name)
	assert.Equal(t, "linear", inst.Kind)
}

func TestResolveByKind_skipsForgeOnly(t *testing.T) {
	instances := []Instance{forgeOnly("prs")}
	_, err := ResolveByKind("github", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no github tracker found")
}

// TestResolveByKind_prefersTrackerOverForgeSameKind confirms that when a github
// tracker and a github forge coexist, tracker resolution lands on the tracker.
func TestResolveByKind_prefersTrackerOverForgeSameKind(t *testing.T) {
	instances := []Instance{
		forgeOnly("prs"),
		{Name: "issues", Kind: "github", Provider: stubProvider{}},
	}
	inst, err := ResolveByKind("github", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "issues", inst.Name)
}

func TestResolveForgeByKind_findsForgeOnly(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "linear", Provider: stubProvider{}},
		forgeOnly("prs"),
	}
	inst, err := ResolveForgeByKind("github", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "prs", inst.Name)
	assert.NotNil(t, inst.Forge)
}

func TestResolveForgeByKind_none(t *testing.T) {
	instances := []Instance{{Name: "work", Kind: "linear", Provider: stubProvider{}}}
	_, err := ResolveForgeByKind("github", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no github forge configured")
}

func TestResolveForgeByKind_withName(t *testing.T) {
	instances := []Instance{forgeOnly("a"), forgeOnly("b")}
	inst, err := ResolveForgeByKind("github", instances, "b")
	require.NoError(t, err)
	assert.Equal(t, "b", inst.Name)

	_, err = ResolveForgeByKind("github", instances, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge name not found")
}

// TestResolveByName_skipsForgeOnly ensures --tracker=<name> resolves the tracker
// even when a forge-only entry shares the name.
func TestResolveByName_skipsForgeOnly(t *testing.T) {
	instances := []Instance{
		forgeOnly("gh"),
		{Name: "gh", Kind: "github", Provider: stubProvider{}},
	}
	inst, err := Resolve("gh", instances, "")
	require.NoError(t, err)
	assert.True(t, inst.IsTracker())
}

// TestFindTracker_skipsForgeOnly guards against probing (and nil-dereferencing)
// a forge-only entry when detecting a key's owning tracker.
func TestFindTracker_skipsForgeOnly(t *testing.T) {
	instances := []Instance{
		{Name: "issues", Kind: "github", Provider: fakeProvider{}},
		forgeOnly("prs"),
	}
	res, err := FindTracker(context.Background(), "octocat/repo#7", instances)
	require.NoError(t, err)
	assert.Equal(t, "github", res.Provider)
}
