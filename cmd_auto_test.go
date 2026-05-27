package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdauto"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/tracker"
)

// setAutoLoader overrides autoInstanceLoader for the duration of a test.
func setAutoLoader(t *testing.T, instances []tracker.Instance) {
	t.Helper()
	orig := autoInstanceLoader
	t.Cleanup(func() { autoInstanceLoader = orig })
	autoInstanceLoader = func() ([]tracker.Instance, error) {
		return instances, nil
	}
}

func newMockInstances(kinds ...string) []tracker.Instance {
	var instances []tracker.Instance
	for _, k := range kinds {
		instances = append(instances, tracker.Instance{
			Name: k + "-inst",
			Kind: k,
			Provider: &mockProvider{
				getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
					return &tracker.Issue{Key: key, Title: "test issue", Status: "Open"}, nil
				},
				listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
					return []tracker.Issue{{Key: "TEST-1", Title: "test", Status: "Open"}}, nil
				},
				createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
					return issue, nil
				},
				deleteIssueFn: func(_ context.Context, _ string) error { return nil },
				listCommentsFn: func(_ context.Context, _ string) ([]tracker.Comment, error) {
					return nil, nil
				},
				addCommentFn: func(_ context.Context, _ string, _ string) (*tracker.Comment, error) {
					return &tracker.Comment{ID: "c1", Body: "ok"}, nil
				},
				listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
					return []tracker.Status{{Name: "Open", Category: "started"}, {Name: "Closed", Category: "closed"}}, nil
				},
				transitionIssueFn: func(_ context.Context, _ string, _ string) error { return nil },
			},
		})
	}
	return instances
}

// --- buildAutoGetCmd tests ---

func TestAutoGet_singleKind(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"get", "KAN-1"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "# KAN-1")
	assert.Contains(t, stderr.String(), "Detected tracker: jira")
	assert.Contains(t, stderr.String(), "human jira issues list --project=KAN")
	assert.Contains(t, stderr.String(), "human jira issue  comment add KAN-1")
}

func TestAutoGet_fallbackToFindTracker(t *testing.T) {
	// Both jira and linear match KAN-1 format — ambiguous for Resolve.
	// FindTracker probes each instance; jira mock succeeds first.
	setAutoLoader(t, newMockInstances("jira", "linear"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"get", "KAN-1"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "# KAN-1")
	// Should resolve to jira (first in probe order).
	assert.Contains(t, stderr.String(), "Detected tracker: jira")
}

func TestAutoGet_withTrackerFlag(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira", "linear"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--tracker=linear-inst", "get", "ENG-1"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "# ENG-1")
	assert.Contains(t, stderr.String(), "Detected tracker: linear")
}

func TestAutoGet_requiresKeyArg(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"get"})

	err := cmd.Execute()
	assert.Error(t, err)
}

func TestAutoGet_noConfiguredTrackers(t *testing.T) {
	setAutoLoader(t, nil)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"get", "KAN-1"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no tracker configured")
}

// --- buildAutoListCmd tests ---

func TestAutoList_singleKind(t *testing.T) {
	setAutoLoader(t, newMockInstances("linear"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--project=HUM"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "Detected tracker: linear")
	assert.Contains(t, stderr.String(), "human linear issue  get <KEY>")
	assert.Contains(t, stderr.String(), `human linear issue  create --project=HUM "Title"`)
}

func TestAutoList_ambiguousMultiKindError(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira", "linear"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--project=HUM"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "multiple tracker types configured")
}

func TestAutoList_withTrackerFlag(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira", "linear"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--tracker=linear-inst", "list", "--project=HUM"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "Detected tracker: linear")
}

func TestAutoList_projectFlagOptional(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestAutoList_allFlag(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--project=KAN", "--all"})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestAutoList_tableFlag(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--project=KAN", "--table"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "KEY")
}

func TestAutoList_noConfiguredTrackers(t *testing.T) {
	setAutoLoader(t, nil)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list", "--project=KAN"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no tracker configured")
}

// --- printAutoHints tests ---

func TestPrintAutoHints_afterGet(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "linear", "HUM-4", "HUM", "get")

	out := buf.String()
	assert.Contains(t, out, "Detected tracker: linear")
	assert.Contains(t, out, "Related commands:")
	assert.Contains(t, out, "human linear issues list --project=HUM")
	assert.Contains(t, out, "human linear issue  comment add HUM-4 'text'")
}

func TestPrintAutoHints_afterList(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "linear", "", "HUM", "list")

	out := buf.String()
	assert.Contains(t, out, "Detected tracker: linear")
	assert.Contains(t, out, "Related commands:")
	assert.Contains(t, out, "human linear issue  get <KEY>")
	assert.Contains(t, out, `human linear issue  create --project=HUM "Title"`)
}

func TestPrintAutoHints_afterGet_includesStatuses(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "jira", "KAN-1", "KAN", "get")

	out := buf.String()
	assert.Contains(t, out, "human jira issue  statuses KAN-1")
}

func TestPrintAutoHints_afterStatuses(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "jira", "KAN-1", "KAN", "statuses")

	out := buf.String()
	assert.Contains(t, out, "Detected tracker: jira")
	assert.Contains(t, out, `human jira issue  status KAN-1 "<STATUS>"`)
}

func TestPrintAutoHints_afterStatus(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "linear", "ENG-1", "ENG", "status")

	out := buf.String()
	assert.Contains(t, out, "Detected tracker: linear")
	assert.Contains(t, out, "human linear issue  statuses ENG-1")
}

func TestPrintAutoHints_afterGet_noProject(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "shortcut", "123", "", "get")

	out := buf.String()
	assert.Contains(t, out, "Detected tracker: shortcut")
	assert.NotContains(t, out, "issues list")
	assert.Contains(t, out, "comment add 123")
}

func TestPrintAutoHints_afterList_noProject(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "jira", "", "", "list")

	out := buf.String()
	assert.Contains(t, out, "human jira issue  get <KEY>")
	assert.NotContains(t, out, "create")
}

func TestPrintAutoHints_getShowsBothHints(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "github", "octocat/repo#42", "octocat/repo", "get")

	out := buf.String()
	assert.Contains(t, out, "human github issues list --project=octocat/repo")
	assert.Contains(t, out, "human github issue  comment add octocat/repo#42 'text'")
}

func TestPrintAutoHints_listShowsBothHints(t *testing.T) {
	var buf bytes.Buffer
	cmdauto.PrintAutoHints(&buf, "jira", "", "KAN", "list")

	out := buf.String()
	assert.Contains(t, out, "human jira issue  get <KEY>")
	assert.Contains(t, out, `human jira issue  create --project=KAN "Title"`)
}

// --- buildAutoStatusesCmd tests ---

func TestAutoStatuses_singleKind(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"statuses", "KAN-1"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Open")
	assert.Contains(t, stderr.String(), "Detected tracker: jira")
}

func TestAutoStatuses_tableFlag(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"statuses", "KAN-1", "--table"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "NAME")
	assert.Contains(t, stdout.String(), "TYPE")
}

func TestAutoStatuses_requiresKeyArg(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"statuses"})

	err := cmd.Execute()
	assert.Error(t, err)
}

// --- buildAutoStatusCmd tests ---

func TestAutoStatus_singleKind(t *testing.T) {
	setAutoLoader(t, newMockInstances("jira"))

	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"status", "KAN-1", "Done"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "Transitioned KAN-1 to Done")
	assert.Contains(t, stderr.String(), "Detected tracker: jira")
}

func TestAutoStatus_requiresArgs(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"status", "KAN-1"})

	err := cmd.Execute()
	assert.Error(t, err)
}

// --- registration tests ---

func TestRootCmd_hasAutoCommands(t *testing.T) {
	cmd := newRootCmd()
	var foundGet, foundList, foundStatuses, foundStatus bool
	for _, sub := range cmd.Commands() {
		switch {
		case strings.HasPrefix(sub.Use, "get "):
			foundGet = true
			assert.Equal(t, "shortcuts", sub.GroupID)
		case sub.Use == "list":
			foundList = true
			assert.Equal(t, "shortcuts", sub.GroupID)
		case strings.HasPrefix(sub.Use, "statuses "):
			foundStatuses = true
			assert.Equal(t, "shortcuts", sub.GroupID)
		case strings.HasPrefix(sub.Use, "status "):
			foundStatus = true
			assert.Equal(t, "shortcuts", sub.GroupID)
		}
	}
	assert.True(t, foundGet, "expected top-level 'get' command")
	assert.True(t, foundList, "expected top-level 'list' command")
	assert.True(t, foundStatuses, "expected top-level 'statuses' command")
	assert.True(t, foundStatus, "expected top-level 'status' command")
}

func TestPrintExamples_includesQuickCommands(t *testing.T) {
	var buf bytes.Buffer
	cmdutil.PrintExamples(&buf)

	out := buf.String()
	assert.Contains(t, out, "Quick commands (auto-detect tracker):")
	assert.Contains(t, out, "human get KAN-1")
	assert.Contains(t, out, "human list --project=KAN")
	assert.Contains(t, out, "human list --project=KAN --tracker=work")
	assert.Contains(t, out, "human statuses KAN-1")
	assert.Contains(t, out, `human status KAN-1 "Done"`)
}

func TestRootHelp_includesQuickCommandsGroup(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})

	origLoader := helpInstanceLoader
	t.Cleanup(func() { helpInstanceLoader = origLoader })
	helpInstanceLoader = func() ([]tracker.Instance, error) { return nil, nil }

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Quick Commands:")
}

// --- auto loader error test ---

func TestAutoGet_loaderError(t *testing.T) {
	orig := autoInstanceLoader
	t.Cleanup(func() { autoInstanceLoader = orig })
	autoInstanceLoader = func() ([]tracker.Instance, error) {
		return nil, fmt.Errorf("config load error")
	}

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"get", "KAN-1"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config load error")
}
