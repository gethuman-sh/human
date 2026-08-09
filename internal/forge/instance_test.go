package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubForge is a minimal Forge for exercising resolution.
type stubForge struct{}

func (stubForge) CreatePullRequest(context.Context, *PullRequest) (*PullRequest, error) {
	return nil, nil
}

func instance(name string) Instance {
	return Instance{Name: name, Kind: "github", Forge: stubForge{}}
}

func TestResolve_byKind(t *testing.T) {
	got, err := Resolve("github", []Instance{instance("prs")}, "")
	require.NoError(t, err)
	assert.Equal(t, "prs", got.Name)
	assert.NotNil(t, got.Forge)
}

func TestResolve_byName(t *testing.T) {
	instances := []Instance{instance("a"), instance("b")}

	got, err := Resolve("github", instances, "b")
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name)

	_, err = Resolve("github", instances, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge name not found")
}

// The error someone actually hits mid-deploy, when a config predating the split
// stops opening pull requests. It has to carry the fix, not just the diagnosis:
// this is what turns "my pipeline broke" into a two-minute edit ([SC-3876]).
func TestResolve_noneConfiguredExplainsTheFix(t *testing.T) {
	_, err := Resolve("github", nil, "")
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "githubs: entry is an issue tracker")
	assert.Contains(t, msg, "forges:")
	assert.Contains(t, msg, "human config migrate")
}

// A forge of another kind is not a substitute: resolution is per code host, so
// a GitLab entry cannot answer for GitHub.
func TestResolve_otherKindDoesNotAnswer(t *testing.T) {
	_, err := Resolve("github", []Instance{{Name: "x", Kind: "gitlab", Forge: stubForge{}}}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no github forge configured")
}
