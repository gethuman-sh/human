package cmdforge

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
)

type stubForge struct{}

func (stubForge) CreatePullRequest(context.Context, *forge.PullRequest) (*forge.PullRequest, error) {
	return nil, nil
}

func loaderOK(instances []forge.Instance) func(string) ([]forge.Instance, error) {
	return func(string) ([]forge.Instance, error) { return instances, nil }
}

func configured() []forge.Instance {
	return []forge.Instance{{Name: "prs", Kind: "github", URL: "https://api.github.com", Forge: stubForge{}}}
}

func TestRunForgeList_JSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunForgeList(&buf, ".", false, loaderOK(configured())))

	out := buf.String()
	assert.Contains(t, out, `"name": "prs"`)
	assert.Contains(t, out, `"kind": "github"`)
	assert.Contains(t, out, `"url": "https://api.github.com"`)
}

func TestRunForgeList_Table(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunForgeList(&buf, ".", true, loaderOK(configured())))

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "prs")
	assert.Contains(t, out, "github")
}

// The empty listing is the one worth getting right: with no forge configured
// nothing can open a pull request, and every deploy stops there. Answering with
// an empty array would be accurate and useless.
func TestRunForgeList_EmptyExplainsTheConsequence(t *testing.T) {
	for _, table := range []bool{false, true} {
		var buf bytes.Buffer
		require.NoError(t, RunForgeList(&buf, ".", table, loaderOK(nil)))

		out := buf.String()
		assert.Contains(t, out, "githubs: entry is an issue tracker")
		assert.Contains(t, out, "human config migrate")
	}
}

func TestRunForgeList_LoaderError(t *testing.T) {
	loader := func(string) ([]forge.Instance, error) {
		return nil, errors.WithDetails("config unreadable", "dir", ".")
	}
	var buf bytes.Buffer
	err := RunForgeList(&buf, ".", false, loader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config unreadable")
}

func TestBuildForgeCmd_hasList(t *testing.T) {
	cmd := BuildForgeCmd(loaderOK(configured()))
	assert.Equal(t, "forge", cmd.Use)

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "list" {
			found = true
		}
	}
	assert.True(t, found)
}
