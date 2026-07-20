package cmdcommits

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

func withCommitsFor(t *testing.T, fn func(ctx context.Context, dir, key string) ([]gitrepo.Commit, error)) {
	t.Helper()
	prev := gitrepo.CommitsFor
	gitrepo.CommitsFor = fn
	t.Cleanup(func() { gitrepo.CommitsFor = prev })
}

func TestRunCommitsFor_JSON(t *testing.T) {
	withCommitsFor(t, func(_ context.Context, dir, key string) ([]gitrepo.Commit, error) {
		assert.Equal(t, ".", dir)
		assert.Equal(t, "SC-57", key)
		return []gitrepo.Commit{{SHA: "aaa", ShortSHA: "a1", Subject: "[SC-57] Add validation"}}, nil
	})

	var buf bytes.Buffer
	err := RunCommitsFor(context.Background(), &buf, ".", "SC-57", false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"sha": "aaa"`)
	assert.Contains(t, out, `"short": "a1"`)
	assert.Contains(t, out, `"subject": "[SC-57] Add validation"`)
}

func TestRunCommitsFor_JSONEmptyIsArray(t *testing.T) {
	withCommitsFor(t, func(_ context.Context, _, _ string) ([]gitrepo.Commit, error) {
		return nil, nil
	})

	var buf bytes.Buffer
	err := RunCommitsFor(context.Background(), &buf, ".", "42", false)
	require.NoError(t, err)
	assert.Equal(t, "[]\n", buf.String())
}

func TestRunCommitsFor_Table(t *testing.T) {
	withCommitsFor(t, func(_ context.Context, _, _ string) ([]gitrepo.Commit, error) {
		return []gitrepo.Commit{{SHA: "aaa", ShortSHA: "a1", Subject: "s1"}}, nil
	})

	var buf bytes.Buffer
	err := RunCommitsFor(context.Background(), &buf, ".", "SC-57", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "SHORT")
	assert.Contains(t, buf.String(), "a1")
}

func TestRunCommitsFor_TableEmpty(t *testing.T) {
	withCommitsFor(t, func(_ context.Context, _, _ string) ([]gitrepo.Commit, error) {
		return nil, nil
	})

	var buf bytes.Buffer
	err := RunCommitsFor(context.Background(), &buf, ".", "SC-57", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No commits reference this key")
}

func TestRunCommitsFor_Error(t *testing.T) {
	withCommitsFor(t, func(_ context.Context, _, _ string) ([]gitrepo.Commit, error) {
		return nil, errors.WithDetails("not a repository", "dir", ".")
	})

	var buf bytes.Buffer
	err := RunCommitsFor(context.Background(), &buf, ".", "SC-57", false)
	require.Error(t, err)
}

func TestRunCommitPrefix_SingleKey(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(&buf, []string{"SC-79"}))
	assert.Equal(t, "[SC-79]\n", buf.String())
}

func TestRunCommitPrefix_SplitTopologyKeys(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(&buf, []string{"SC-79", "HUM-59"}))
	assert.Equal(t, "[SC-79] [HUM-59]\n", buf.String())
}

func TestRunCommitPrefix_AlreadyBracketed(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(&buf, []string{"[SC-79]", " HUM-59 "}))
	assert.Equal(t, "[SC-79] [HUM-59]\n", buf.String())
}

func TestBuildCommitsCmd_Subcommands(t *testing.T) {
	cmd := BuildCommitsCmd()
	uses := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		uses = append(uses, sub.Use)
	}
	assert.Contains(t, uses, "for KEY")
	assert.Contains(t, uses, "prefix KEY [ENGINEERING_KEY]")
}
