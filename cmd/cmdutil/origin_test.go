package cmdutil

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/github"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/tracker"
)

func stubOrigin(t *testing.T, url string, err error) {
	t.Helper()
	prev := gitrepo.OriginURL
	gitrepo.OriginURL = func(_ context.Context, _ string) (string, error) { return url, err }
	t.Cleanup(func() { gitrepo.OriginURL = prev })
}

func githubDeps() Deps {
	return Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return []tracker.Instance{{
				Name:     "gh",
				Kind:     "github",
				URL:      "https://api.github.com",
				Provider: github.New("https://api.github.com", "t"),
			}}, nil
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
		AuditLogPath:      func() string { return "" },
	}
}

func TestOriginForge_success(t *testing.T) {
	stubOrigin(t, "git@github.com:gethuman-sh/human.git", nil)

	f, repo, err := OriginForge(&cobra.Command{}, githubDeps())
	require.NoError(t, err)
	assert.Equal(t, "gethuman-sh/human", repo)
	assert.NotNil(t, f)
}

func TestOriginForge_unsupportedHost(t *testing.T) {
	stubOrigin(t, "git@bitbucket.org:foo/bar.git", nil)

	_, _, err := OriginForge(&cobra.Command{}, githubDeps())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge")
}

func TestOriginForge_gitError(t *testing.T) {
	stubOrigin(t, "", errors.New("no origin"))

	_, _, err := OriginForge(&cobra.Command{}, githubDeps())
	require.Error(t, err)
}

func TestOriginRepo_success(t *testing.T) {
	stubOrigin(t, "https://github.com/gethuman-sh/human.git", nil)

	repo, err := OriginRepo(&cobra.Command{})
	require.NoError(t, err)
	assert.Equal(t, "gethuman-sh/human", repo)
}

func TestOriginRepo_gitError(t *testing.T) {
	stubOrigin(t, "", errors.New("no origin"))

	_, err := OriginRepo(&cobra.Command{})
	require.Error(t, err)
}
