package cmdlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/local"
)

func TestRunHistory_printsMarkersAndEvents(t *testing.T) {
	c, err := local.OpenMemory("LOC", "Ada")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	issue, err := c.CreateIssue(ctx, &tracker.Issue{Title: "x"})
	require.NoError(t, err)
	_, err = c.AddComment(ctx, issue.Key, "[human:planning-started]\ndaemon: d1")
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, RunHistory(ctx, c, &out, issue.Key))

	var h History
	require.NoError(t, json.Unmarshal(out.Bytes(), &h))
	assert.Equal(t, issue.Key, h.Key)
	require.Len(t, h.Markers, 1)
	assert.Equal(t, "planning-started", h.Markers[0].Type)
	assert.Equal(t, "d1", h.Markers[0].Fields["daemon"])
	require.Len(t, h.Events, 2)
	assert.Equal(t, "created", h.Events[0].Kind)
	assert.Equal(t, "marker", h.Events[1].Kind)

	assert.Error(t, RunHistory(ctx, c, &out, "LOC-99"))
}

func TestRunHistory_emptyHistoryIsEmptyArrays(t *testing.T) {
	c, err := local.OpenMemory("LOC", "Ada")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	issue, err := c.CreateIssue(context.Background(), &tracker.Issue{Title: "x"})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, RunHistory(context.Background(), c, &out, issue.Key))
	assert.Contains(t, out.String(), `"markers": []`)
	assert.NotContains(t, out.String(), "null")
}

type plainProvider struct{ tracker.Provider }

func TestRunHistory_refusesATrackerThatDoesNotIndex(t *testing.T) {
	var out bytes.Buffer
	err := RunHistory(context.Background(), plainProvider{}, &out, "SC-1")
	assert.Error(t, err)
	assert.Empty(t, out.String())
}

func TestBuildLocalCommands_hasHistory(t *testing.T) {
	cmds := BuildLocalCommands(cmdutil.DefaultDeps())
	require.Len(t, cmds, 1)
	assert.Equal(t, "history KEY", cmds[0].Use)
}
