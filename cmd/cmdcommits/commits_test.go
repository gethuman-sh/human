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

// withCommitKindFor stubs the commitKindFor seam so tests do not depend on
// real config/vault resolution.
func withCommitKindFor(t *testing.T, kind string) {
	t.Helper()
	prev := commitKindFor
	commitKindFor = func(context.Context, string) string { return kind }
	t.Cleanup(func() { commitKindFor = prev })
}

func TestRunCommitPrefix_SingleKey(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"SC-79"}))
	assert.Equal(t, "[SC-79]\n", buf.String())
}

func TestRunCommitPrefix_SplitTopologyKeys(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"SC-79", "HUM-59"}))
	assert.Equal(t, "[SC-79] [HUM-59]\n", buf.String())
}

func TestRunCommitPrefix_AlreadyBracketed(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"[SC-79]", " HUM-59 "}))
	assert.Equal(t, "[SC-79] [HUM-59]\n", buf.String())
}

func TestRunCommitPrefix_BareNumericKeyCanonicalized(t *testing.T) {
	// The board/pipeline passes bare numeric keys internally; the prefix must
	// resolve them to the canonical "SC-nnn" form so agent commits stay
	// attributable and hook-compliant (SC-1134) — but only on a Shortcut
	// workspace.
	withCommitKindFor(t, "shortcut")
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"1117"}))
	assert.Equal(t, "[SC-1117]\n", buf.String())
}

func TestRunCommitPrefix_BareNumericInBrackets(t *testing.T) {
	withCommitKindFor(t, "shortcut")
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"[1117]"}))
	assert.Equal(t, "[SC-1117]\n", buf.String())
}

// TestRunCommitPrefix_BareNumericNonShortcutUntouched is the regression test
// for SC-2855: a bare numeric key on a non-Shortcut workspace (e.g. ClickUp)
// must not be guessed into Shortcut's "SC-" prefix.
func TestRunCommitPrefix_BareNumericNonShortcutUntouched(t *testing.T) {
	withCommitKindFor(t, "clickup")
	var buf bytes.Buffer
	require.NoError(t, RunCommitPrefix(context.Background(), &buf, []string{"90210"}))
	assert.Equal(t, "[90210]\n", buf.String())
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

func TestRunCommitKeys_JSON(t *testing.T) {
	prev := gitrepo.TicketKeys
	gitrepo.TicketKeys = func(_ context.Context, _ string, paths []string) ([]string, error) {
		assert.Equal(t, []string{"internal/tracker"}, paths)
		return []string{"SC-881", "42"}, nil
	}
	t.Cleanup(func() { gitrepo.TicketKeys = prev })

	var buf bytes.Buffer
	err := RunCommitKeys(context.Background(), &buf, ".", []string{"internal/tracker"})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"SC-881"`)
	assert.Contains(t, buf.String(), `"42"`)
}

func TestRunCommitKeys_emptyIsArray(t *testing.T) {
	prev := gitrepo.TicketKeys
	gitrepo.TicketKeys = func(context.Context, string, []string) ([]string, error) { return nil, nil }
	t.Cleanup(func() { gitrepo.TicketKeys = prev })

	var buf bytes.Buffer
	require.NoError(t, RunCommitKeys(context.Background(), &buf, ".", nil))
	assert.Equal(t, "[]\n", buf.String())
}

func TestRunCommitsRecency_tagAndFallback(t *testing.T) {
	prev := gitrepo.LatestTag
	t.Cleanup(func() { gitrepo.LatestTag = prev })

	gitrepo.LatestTag = func(context.Context, string) string { return "v0.21.0" }
	var buf bytes.Buffer
	require.NoError(t, RunCommitsRecency(context.Background(), &buf, "."))
	assert.Contains(t, buf.String(), `"tag": "v0.21.0"`)

	gitrepo.LatestTag = func(context.Context, string) string { return "" }
	buf.Reset()
	require.NoError(t, RunCommitsRecency(context.Background(), &buf, "."))
	assert.Contains(t, buf.String(), `"since": "30 days ago"`)
}

func TestRunCommitsTouched_usesResolvedBoundary(t *testing.T) {
	prevTag, prevTouched := gitrepo.LatestTag, gitrepo.TouchedSince
	t.Cleanup(func() { gitrepo.LatestTag, gitrepo.TouchedSince = prevTag, prevTouched })

	gitrepo.LatestTag = func(context.Context, string) string { return "v1.0.0" }
	var gotBoundary string
	gitrepo.TouchedSince = func(_ context.Context, _, boundary string, _ []string) (bool, error) {
		gotBoundary = boundary
		return true, nil
	}

	var buf bytes.Buffer
	require.NoError(t, RunCommitsTouched(context.Background(), &buf, ".", "", []string{"cmd"}))
	assert.Equal(t, "v1.0.0", gotBoundary)
	assert.Equal(t, "true\n", buf.String())
}

func TestRunCommitsTouched_explicitRefWins(t *testing.T) {
	prevTag, prevTouched := gitrepo.LatestTag, gitrepo.TouchedSince
	t.Cleanup(func() { gitrepo.LatestTag, gitrepo.TouchedSince = prevTag, prevTouched })

	gitrepo.LatestTag = func(context.Context, string) string { return "v1.0.0" }
	var gotBoundary string
	gitrepo.TouchedSince = func(_ context.Context, _, boundary string, _ []string) (bool, error) {
		gotBoundary = boundary
		return false, nil
	}

	var buf bytes.Buffer
	require.NoError(t, RunCommitsTouched(context.Background(), &buf, ".", "v0.9.0", nil))
	assert.Equal(t, "v0.9.0", gotBoundary)
	assert.Equal(t, "false\n", buf.String())
}
