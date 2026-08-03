package cmdunderway

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/gethuman-sh/human/internal/forge"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunUnderway_reportsOpenWork(t *testing.T) {
	origFind := findOpenWork
	defer func() { findOpenWork = origFind }()
	origKind := commitKindFor
	defer func() { commitKindFor = origKind }()
	commitKindFor = func(context.Context, string) string { return "shortcut" }
	findOpenWork = func(_ context.Context, key string) ([]forge.OpenWork, string, error) {
		assert.Equal(t, "SC-2648", key) // canonicalized from bare "2648"
		return []forge.OpenWork{{Kind: "pull-request", Number: 57, URL: "https://x/pull/57", Title: "[SC-2648] fix"}}, "octocat/hello-world", nil
	}
	var buf bytes.Buffer
	require.NoError(t, RunUnderway(context.Background(), &buf, "2648"))
	var got underwayResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.True(t, got.Underway)
	assert.Equal(t, "SC-2648", got.Key)
	require.Len(t, got.Work, 1)
	assert.Equal(t, 57, got.Work[0].Number)
}

// TestRunUnderway_numericNonShortcutUntouched is the regression test for
// SC-2855: a bare numeric key on a non-Shortcut workspace must not be
// canonicalized into Shortcut's "SC-" prefix.
func TestRunUnderway_numericNonShortcutUntouched(t *testing.T) {
	origFind := findOpenWork
	defer func() { findOpenWork = origFind }()
	origKind := commitKindFor
	defer func() { commitKindFor = origKind }()
	commitKindFor = func(context.Context, string) string { return "" }
	findOpenWork = func(_ context.Context, key string) ([]forge.OpenWork, string, error) {
		assert.Equal(t, "2648", key)
		return nil, "octocat/hello-world", nil
	}
	var buf bytes.Buffer
	require.NoError(t, RunUnderway(context.Background(), &buf, "2648"))
	var got underwayResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "2648", got.Key)
}

func TestRunUnderway_nothingOpen(t *testing.T) {
	orig := findOpenWork
	defer func() { findOpenWork = orig }()
	findOpenWork = func(_ context.Context, _ string) ([]forge.OpenWork, string, error) {
		return nil, "octocat/hello-world", nil
	}
	var buf bytes.Buffer
	require.NoError(t, RunUnderway(context.Background(), &buf, "SC-2648"))
	var got underwayResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.False(t, got.Underway)
	assert.Equal(t, []forge.OpenWork{}, got.Work) // never null
}
