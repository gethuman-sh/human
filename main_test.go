package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdprovider"
	"github.com/gethuman-sh/human/cmd/cmdtracker"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

func TestAuditLogPath(t *testing.T) {
	p := cmdutil.AuditLogPath()
	assert.Contains(t, p, ".human")
	assert.Contains(t, p, "audit.log")
	assert.True(t, filepath.IsAbs(p), "expected absolute path, got %s", p)
}

// --- help / printExamples tests ---

func TestRootHelp_includesExamples(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})

	origLoader := helpInstanceLoader
	t.Cleanup(func() { helpInstanceLoader = origLoader })
	helpInstanceLoader = func() ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", URL: "https://work.atlassian.net", User: "me@work.com", Description: "Sprint planning"},
		}, nil
	}

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Examples:")
	assert.Contains(t, out, "Connected trackers:")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "Sprint planning")
}

func TestSubcommandHelp_noExamples(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"jira", "--help"})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.NotContains(t, out, "Examples:")
}

func TestPrintExamples(t *testing.T) {
	var buf bytes.Buffer
	cmdutil.PrintExamples(&buf)

	out := buf.String()

	// Command pattern section.
	assert.Contains(t, out, "Command pattern:")
	assert.Contains(t, out, "human <tracker> issues list")
	assert.Contains(t, out, "human <tracker> issue  get")
	assert.Contains(t, out, "human <tracker> issue  create")
	assert.Contains(t, out, `human <tracker> issue  edit <KEY> --title "New" --description "Updated"`)
	assert.Contains(t, out, "human <tracker> issue  start <KEY>                Start working on issue")
	assert.Contains(t, out, "human <tracker> issue  statuses <KEY>             List available statuses")
	assert.Contains(t, out, `human <tracker> issue  status <KEY> "<STATUS>"    Set issue status`)
	assert.Contains(t, out, "human <tracker> issue  delete <KEY>               Show confirmation code")
	assert.Contains(t, out, "human <tracker> issue  delete <KEY> --confirm=N   Delete/close issue")
	assert.Contains(t, out, "human <tracker> issue  comment add")
	assert.Contains(t, out, "human <tracker> issue  comment list")

	// Key format reference table — all providers present.
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "github")
	assert.Contains(t, out, "gitlab")
	assert.Contains(t, out, "linear")
	assert.Contains(t, out, "azuredevops")
	assert.Contains(t, out, "shortcut")
	assert.Contains(t, out, "KAN-1")
	assert.Contains(t, out, "octocat/hello-world#42")
	assert.Contains(t, out, "ENG-123")

	// Concrete examples.
	assert.Contains(t, out, "Examples:")
	assert.Contains(t, out, "human jira issues list --project=KAN")
	assert.Contains(t, out, "human jira issue get KAN-1")
	assert.Contains(t, out, `human jira issue create --project=KAN "Implement login page"`)
	assert.Contains(t, out, "human github issues list --project=octocat/hello-world")
	assert.Contains(t, out, `human jira issue edit KAN-1 --title "Updated title"`)
	assert.Contains(t, out, "human jira issue start KAN-1")
	assert.Contains(t, out, "human jira issue statuses KAN-1")
	assert.Contains(t, out, `human jira issue status KAN-1 "Done"`)
	assert.Contains(t, out, "human jira issue delete KAN-1")
	assert.Contains(t, out, "human jira issue delete KAN-1 --confirm=4521")
	assert.Contains(t, out, "human tracker list")
	assert.Contains(t, out, "human install --agent claude")
	assert.Contains(t, out, "human browser https://example.com")
	assert.Contains(t, out, "human daemon start")
	assert.Contains(t, out, "human daemon token")
	assert.Contains(t, out, "human daemon status")
}

func TestPrintExamples_startsWithBlankLine(t *testing.T) {
	var buf bytes.Buffer
	cmdutil.PrintExamples(&buf)
	assert.True(t, strings.HasPrefix(buf.String(), "\n"), "output should start with a blank line separator")
}

func TestPrintConnectedTrackers_withInstances(t *testing.T) {
	loader := func() ([]tracker.Instance, error) {
		return []tracker.Instance{
			{Name: "work", Kind: "jira", URL: "https://work.atlassian.net", User: "me@work.com", Description: "Sprint planning"},
			{Name: "personal", Kind: "github", URL: "https://api.github.com", Description: "OSS projects"},
		}, nil
	}

	var buf bytes.Buffer
	cmdutil.PrintConnectedTrackers(&buf, loader)

	out := buf.String()
	assert.Contains(t, out, "Connected trackers:")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "https://work.atlassian.net")
	assert.Contains(t, out, "me@work.com")
	assert.Contains(t, out, "Sprint planning")
	assert.Contains(t, out, "personal")
	assert.Contains(t, out, "github")
	assert.Contains(t, out, "OSS projects")
}

func TestPrintConnectedTrackers_empty(t *testing.T) {
	loader := func() ([]tracker.Instance, error) {
		return nil, nil
	}

	var buf bytes.Buffer
	cmdutil.PrintConnectedTrackers(&buf, loader)

	out := buf.String()
	assert.Contains(t, out, "Connected trackers: none")
	assert.Contains(t, out, ".humanconfig.yaml")
}

func TestPrintConnectedTrackers_error(t *testing.T) {
	loader := func() ([]tracker.Instance, error) {
		return nil, fmt.Errorf("config error")
	}

	var buf bytes.Buffer
	cmdutil.PrintConnectedTrackers(&buf, loader)

	assert.Empty(t, buf.String(), "errors should be silently ignored")
}

// --- mock provider ---

type mockProvider struct {
	listIssuesFn      func(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error)
	getIssueFn        func(ctx context.Context, key string) (*tracker.Issue, error)
	createIssueFn     func(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error)
	deleteIssueFn     func(ctx context.Context, key string) error
	listCommentsFn    func(ctx context.Context, issueKey string) ([]tracker.Comment, error)
	addCommentFn      func(ctx context.Context, issueKey string, body string) (*tracker.Comment, error)
	transitionIssueFn func(ctx context.Context, key string, targetStatus string) error
	assignIssueFn     func(ctx context.Context, key string, userID string) error
	getCurrentUserFn  func(ctx context.Context) (string, error)
	editIssueFn       func(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error)
	listStatusesFn    func(ctx context.Context, key string) ([]tracker.Status, error)
	linkIssuesFn      func(ctx context.Context, key string, otherKey string) error
}

func (m *mockProvider) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	return m.listIssuesFn(ctx, opts)
}

func (m *mockProvider) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	return m.getIssueFn(ctx, key)
}

func (m *mockProvider) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	return m.createIssueFn(ctx, issue)
}

func (m *mockProvider) DeleteIssue(ctx context.Context, key string) error {
	return m.deleteIssueFn(ctx, key)
}

func (m *mockProvider) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	return m.listCommentsFn(ctx, issueKey)
}

func (m *mockProvider) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	return m.addCommentFn(ctx, issueKey, body)
}

func (m *mockProvider) LinkIssues(ctx context.Context, key string, otherKey string) error {
	if m.linkIssuesFn == nil {
		return nil
	}
	return m.linkIssuesFn(ctx, key, otherKey)
}

func (m *mockProvider) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	if m.transitionIssueFn != nil {
		return m.transitionIssueFn(ctx, key, targetStatus)
	}
	return nil
}

func (m *mockProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	if m.assignIssueFn != nil {
		return m.assignIssueFn(ctx, key, userID)
	}
	return nil
}

func (m *mockProvider) GetCurrentUser(ctx context.Context) (string, error) {
	if m.getCurrentUserFn != nil {
		return m.getCurrentUserFn(ctx)
	}
	return "", nil
}

func (m *mockProvider) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	if m.editIssueFn != nil {
		return m.editIssueFn(ctx, key, opts)
	}
	return nil, nil
}

func (m *mockProvider) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	if m.listStatusesFn != nil {
		return m.listStatusesFn(ctx, key)
	}
	return nil, nil
}

// --- print function tests ---

func TestPrintTrackerJSON(t *testing.T) {
	entries := []cmdtracker.TrackerEntry{
		{Name: "work", Type: "jira", URL: "https://example.atlassian.net", User: "alice"},
	}

	var buf bytes.Buffer
	err := cmdtracker.PrintTrackerJSON(&buf, entries)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "// Configured issue trackers")
	assert.Contains(t, out, `"name": "work"`)
	assert.Contains(t, out, `"type": "jira"`)
}

func TestPrintTrackerTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := cmdtracker.PrintTrackerTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No trackers configured")
}

func TestPrintTrackerTable_withEntries(t *testing.T) {
	entries := []cmdtracker.TrackerEntry{
		{Name: "work", Type: "jira", URL: "https://example.atlassian.net", User: "alice"},
		{Name: "oss", Type: "github", URL: "https://api.github.com"},
	}

	var buf bytes.Buffer
	err := cmdtracker.PrintTrackerTable(&buf, entries)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "oss")
	assert.Contains(t, out, "github")
}

func TestPrintIssuesJSON(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "First issue", Status: "Open"},
	}

	var buf bytes.Buffer
	err := cmdutil.PrintJSON(&buf, issues)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"key": "KAN-1"`)
	assert.Contains(t, out, `"title": "First issue"`)
}

func TestPrintIssuesTable(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Status: "Open", Title: "First issue"},
		{Key: "KAN-2", Status: "Done", Title: "Second issue"},
	}

	var buf bytes.Buffer
	err := cmdutil.PrintJSON(&buf, issues)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "KAN-1")
	assert.Contains(t, out, "KAN-2")
}

// --- loadAllInstances tests ---

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func TestLoadAllInstances_noConfig(t *testing.T) {
	dir := t.TempDir()
	instances, err := cmdutil.LoadAllInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadAllInstances_withJira(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `jiras:
  - name: work
    url: https://work.atlassian.net
    user: me@work.com
    key: tok1
`)
	instances, err := cmdutil.LoadAllInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "jira", instances[0].Kind)
}

func TestLoadAllInstances_multipleProviders(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `jiras:
  - name: work
    url: https://work.atlassian.net
    user: me@work.com
    key: tok1
githubs:
  - name: oss
    token: ghp_test
azuredevops:
  - name: devops
    org: myorg
    token: pat-test
`)

	unsetAzureEnvs(t)

	instances, err := cmdutil.LoadAllInstances(dir)
	require.NoError(t, err)
	assert.Len(t, instances, 3)
}

func unsetAzureEnvs(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AZURE_URL", "AZURE_ORG", "AZURE_TOKEN"} {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
}

// --- Business logic function tests ---

func TestRunListIssues_JSON(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "First", Status: "Open"},
	}
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			assert.Equal(t, "KAN", opts.Project)
			assert.Equal(t, 50, opts.MaxResults)
			assert.False(t, opts.IncludeAll)
			return issues, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListIssues(context.Background(), p, &buf, "KAN", false, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"key": "KAN-1"`)
}

func TestRunListIssues_All(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "Open", Status: "Open"},
		{Key: "KAN-2", Title: "Done", Status: "Done"},
	}
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			assert.True(t, opts.IncludeAll)
			return issues, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListIssues(context.Background(), p, &buf, "KAN", true, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"key": "KAN-1"`)
	assert.Contains(t, buf.String(), `"key": "KAN-2"`)
}

func TestRunListIssues_Table(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "KAN-1", Title: "First", Status: "Open"},
	}
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return issues, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListIssues(context.Background(), p, &buf, "KAN", false, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "KEY")
}

func TestRunListIssues_error(t *testing.T) {
	p := &mockProvider{
		listIssuesFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return nil, fmt.Errorf("list failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListIssues(context.Background(), p, &buf, "KAN", false, false)
	assert.EqualError(t, err, "list failed")
}

func TestRunGetIssue(t *testing.T) {
	issue := &tracker.Issue{
		Key:         "KAN-1",
		Title:       "Test issue",
		Status:      "In Progress",
		Priority:    "High",
		Assignee:    "alice",
		Reporter:    "bob",
		Description: "Some description",
	}
	p := &mockProvider{
		getIssueFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			return issue, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunGetIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)

	// Assert on semantic content, not exact table spacing, so a
	// whitespace-only formatter tweak does not break the test.
	out := buf.String()
	assert.Contains(t, out, "KAN-1")
	assert.Contains(t, out, "Test issue")
	assert.Contains(t, out, "Status")
	assert.Contains(t, out, "In Progress")
	assert.Contains(t, out, "Priority")
	assert.Contains(t, out, "High")
	assert.Contains(t, out, "Assignee")
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "Reporter")
	assert.Contains(t, out, "bob")
	assert.Contains(t, out, "Description")
	assert.Contains(t, out, "Some description")
}

func TestRunGetIssue_emptyFields(t *testing.T) {
	issue := &tracker.Issue{
		Key:    "KAN-2",
		Title:  "Minimal",
		Status: "Open",
	}
	p := &mockProvider{
		getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			return issue, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunGetIssue(context.Background(), p, &buf, "KAN-2")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "| Priority | None |")
	assert.Contains(t, out, "| Assignee | None |")
	assert.Contains(t, out, "| Reporter | None |")
	assert.NotContains(t, out, "## Description")
}

func TestRunGetIssue_error(t *testing.T) {
	p := &mockProvider{
		getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			return nil, fmt.Errorf("get failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunGetIssue(context.Background(), p, &buf, "KAN-1")
	assert.EqualError(t, err, "get failed")
}

func TestRunCreateIssue(t *testing.T) {
	p := &mockProvider{
		createIssueFn: func(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
			assert.Equal(t, "KAN", issue.Project)
			assert.Equal(t, "Task", issue.Type)
			assert.Equal(t, "New issue", issue.Title)
			return &tracker.Issue{Key: "KAN-42", Title: "New issue"}, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunCreateIssue(context.Background(), p, &buf, "KAN", "Task", "New issue", "", "", nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-42")
	assert.Contains(t, buf.String(), "New issue")
}

func TestRunCreateIssue_error(t *testing.T) {
	p := &mockProvider{
		createIssueFn: func(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
			return nil, fmt.Errorf("create failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunCreateIssue(context.Background(), p, &buf, "KAN", "Task", "X", "", "", nil)
	assert.EqualError(t, err, "create failed")
}

func TestRunDeleteIssue(t *testing.T) {
	key := "KAN-1"
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, k string) error {
			assert.Equal(t, key, k)
			return nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunDeleteIssue(context.Background(), p, strings.NewReader(""), &buf, key, true)
	require.NoError(t, err)
	assert.Equal(t, "Deleted KAN-1\n", buf.String())
}

func TestRunDeleteIssue_error(t *testing.T) {
	key := "KAN-ERR"
	p := &mockProvider{
		deleteIssueFn: func(_ context.Context, _ string) error {
			return fmt.Errorf("delete failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunDeleteIssue(context.Background(), p, strings.NewReader(""), &buf, key, true)
	assert.EqualError(t, err, "delete failed")
}

func TestRunAddComment(t *testing.T) {
	p := &mockProvider{
		addCommentFn: func(_ context.Context, issueKey string, body string) (*tracker.Comment, error) {
			assert.Equal(t, "KAN-1", issueKey)
			assert.Equal(t, "test comment", body)
			return &tracker.Comment{ID: "c-1", Body: "test comment"}, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunAddComment(context.Background(), p, &buf, "KAN-1", "test comment")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "c-1")
	assert.Contains(t, buf.String(), "test comment")
}

func TestRunAddComment_error(t *testing.T) {
	p := &mockProvider{
		addCommentFn: func(_ context.Context, _ string, _ string) (*tracker.Comment, error) {
			return nil, fmt.Errorf("comment failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunAddComment(context.Background(), p, &buf, "KAN-1", "x")
	assert.EqualError(t, err, "comment failed")
}

func TestRunListComments(t *testing.T) {
	comments := []tracker.Comment{
		{ID: "c-1", Author: "alice", Body: "hello"},
		{ID: "c-2", Author: "bob", Body: "world"},
	}
	p := &mockProvider{
		listCommentsFn: func(_ context.Context, issueKey string) ([]tracker.Comment, error) {
			assert.Equal(t, "KAN-1", issueKey)
			return comments, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListComments(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "c-1")
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "c-2")
}

func TestRunListComments_error(t *testing.T) {
	p := &mockProvider{
		listCommentsFn: func(_ context.Context, _ string) ([]tracker.Comment, error) {
			return nil, fmt.Errorf("list comments failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListComments(context.Background(), p, &buf, "KAN-1")
	assert.EqualError(t, err, "list comments failed")
}

func TestRunStartIssue(t *testing.T) {
	p := &mockProvider{
		getCurrentUserFn: func(_ context.Context) (string, error) {
			return "user-123", nil
		},
		transitionIssueFn: func(_ context.Context, key string, targetStatus string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "In Progress", targetStatus)
			return nil
		},
		assignIssueFn: func(_ context.Context, key string, userID string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "user-123", userID)
			return nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunStartIssue(context.Background(), p, &buf, "KAN-1")
	require.NoError(t, err)
	assert.Equal(t, "Started KAN-1\n", buf.String())
}

// Each case exercises exactly one failure path in the transition/assign
// pair. Single-failure cases succeed overall (the user is not blocked)
// but the partial failure is logged; "both fail" returns an error;
// "getCurrentUser fails" returns before either mutation runs.
func TestRunStartIssue_partialFailures(t *testing.T) {
	tests := []struct {
		name            string
		getUserErr      error
		transitionErr   error
		assignErr       error
		wantErrContains string
		wantStdout      []string
	}{
		{
			name:          "transition fails but assign succeeds",
			transitionErr: fmt.Errorf("no transition"),
			wantStdout:    []string{"Assigned KAN-1 to user-123", "transition failed"},
		},
		{
			name:       "assign fails but transition succeeds",
			assignErr:  fmt.Errorf("assign denied"),
			wantStdout: []string{"Transitioned KAN-1 to In Progress", "assign failed"},
		},
		{
			name:            "both fail returns error",
			transitionErr:   fmt.Errorf("no transition"),
			assignErr:       fmt.Errorf("assign denied"),
			wantErrContains: "failed to start issue",
		},
		{
			name:            "getCurrentUser fails returns error",
			getUserErr:      fmt.Errorf("auth failed"),
			wantErrContains: "getting current user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockProvider{
				getCurrentUserFn: func(_ context.Context) (string, error) {
					if tt.getUserErr != nil {
						return "", tt.getUserErr
					}
					return "user-123", nil
				},
				transitionIssueFn: func(_ context.Context, _, _ string) error {
					return tt.transitionErr
				},
				assignIssueFn: func(_ context.Context, _, _ string) error {
					return tt.assignErr
				},
			}

			var buf bytes.Buffer
			err := cmdprovider.RunStartIssue(context.Background(), p, &buf, "KAN-1")
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			for _, s := range tt.wantStdout {
				assert.Contains(t, buf.String(), s)
			}
		})
	}
}

func TestRunTrackerList_JSON(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `jiras:
  - name: work
    url: https://work.atlassian.net
    user: me@work.com
    key: tok1
    description: Sprint planning
`)

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerList(&buf, dir, false, cmdutil.LoadAllInstances)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "// Configured issue trackers")
	assert.Contains(t, out, `"name": "work"`)
	assert.Contains(t, out, `"type": "jira"`)
	assert.Contains(t, out, `"description": "Sprint planning"`)
}

func TestRunTrackerList_Table(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `jiras:
  - name: work
    url: https://work.atlassian.net
    user: me@work.com
    key: tok1
    description: Sprint planning
`)

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerList(&buf, dir, true, cmdutil.LoadAllInstances)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "DESCRIPTION")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "Sprint planning")
}

func TestRunTrackerList_empty(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerList(&buf, dir, false, cmdutil.LoadAllInstances)
	require.NoError(t, err)

	// Empty list => prints JSON with empty array
	out := buf.String()
	assert.Contains(t, out, "[]")
}

func TestRunTrackerList_defaultDir(t *testing.T) {
	// When Dir is empty, defaults to "." — use a clean temp dir to avoid
	// picking up a real .humanconfig from the repo root.
	dir := t.TempDir()
	t.Chdir(dir)

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerList(&buf, "", false, cmdutil.LoadAllInstances)
	require.NoError(t, err)
	// Output should contain something (either trackers or empty)
	assert.True(t, strings.Contains(buf.String(), "//") || strings.Contains(buf.String(), "[]"))
}

// --- newRootCmd tests ---

func TestRootCmd_defaultShowsHelp(t *testing.T) {
	// When invoked without args, root command shows help
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	require.NoError(t, err)
	// Should produce help output with usage info
	assert.Contains(t, buf.String(), "Usage:")
	assert.Contains(t, buf.String(), "human")
}

// --- instanceFromFlags tests ---

func TestInstanceFromFlags_noFlags(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{})
	_ = cmd.Execute()
	inst := cmdutil.InstanceFromFlags(cmd)
	assert.Nil(t, inst)
}

// --- tracker find tests ---

func TestRunTrackerFindWithInstances_JSON(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Provider: &mockProvider{
			getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
				return &tracker.Issue{Key: "KAN-42"}, nil
			},
		}},
	}

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerFindWithInstances(context.Background(), &buf, "KAN-42", instances, false)
	require.NoError(t, err)

	var result map[string]string
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result))
	assert.Equal(t, "jira", result["provider"])
	assert.Equal(t, "KAN", result["project"])
	assert.Equal(t, "KAN-42", result["key"])
}

func TestRunTrackerFindWithInstances_Table(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Provider: &mockProvider{
			getIssueFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
				return &tracker.Issue{Key: "KAN-42"}, nil
			},
		}},
	}

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerFindWithInstances(context.Background(), &buf, "KAN-42", instances, true)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PROVIDER")
	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "KEY")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "KAN")
	assert.Contains(t, out, "KAN-42")
}

func TestIsLocalSubcommand(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"daemon", "token"}, true},
		{[]string{"daemon", "start"}, true},
		{[]string{"daemon"}, true},
		{[]string{"--verbose", "daemon", "token"}, true},
		{[]string{"jira", "issues", "list"}, false},
		{[]string{"--help"}, false},
		{[]string{"--version"}, true},
		{[]string{"-v"}, true},
		{[]string{"--", "daemon"}, false},
		{[]string{"chrome-bridge"}, true},
		{[]string{"--verbose", "chrome-bridge"}, true},
		{[]string{"install", "--agent", "claude"}, true},
		{[]string{"--verbose", "install"}, true},
		{[]string{"init"}, true},
		{[]string{"--verbose", "init"}, true},
		{[]string{"hook"}, true},
		{[]string{"--verbose", "hook"}, true},
		// Space-separated value-taking flags must be skipped over (C-LOGIC-025).
		{[]string{"--tracker", "work", "daemon", "stop"}, true},
		{[]string{"--tracker=work", "daemon", "stop"}, true},
		{[]string{"--github-token", "ghp_xx", "install"}, true},
		{[]string{"--clickup-token", "X", "index"}, true},
		{[]string{"--tracker", "work", "tui"}, true},
		{[]string{"--tracker", "work", "init"}, true},
		// Tracker-forwarded commands should stay forwarded even with space flags.
		{[]string{"--tracker", "work", "jira", "issue", "get", "KAN-1"}, false},
		{[]string{"--tracker", "work", "linear", "issues", "list"}, false},
	}
	for _, tt := range tests {
		got := isLocalSubcommand(tt.args)
		assert.Equal(t, tt.want, got, "isLocalSubcommand(%v)", tt.args)
	}
}

func TestRunEditIssue(t *testing.T) {
	title := "Updated"
	p := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, &title, opts.Title)
			assert.Nil(t, opts.Description)
			return &tracker.Issue{Key: "KAN-1", Title: "Updated"}, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{Title: &title})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "Updated")
}

func TestRunEditIssue_error(t *testing.T) {
	title := "X"
	p := &mockProvider{
		editIssueFn: func(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
			return nil, fmt.Errorf("edit failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{Title: &title})
	assert.EqualError(t, err, "edit failed")
}

func TestRunEditIssue_bothFields(t *testing.T) {
	title := "New Title"
	desc := "New Desc"
	p := &mockProvider{
		editIssueFn: func(_ context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, &title, opts.Title)
			assert.Equal(t, &desc, opts.Description)
			return &tracker.Issue{Key: "KAN-1", Title: "New Title"}, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunEditIssue(context.Background(), p, &buf, "KAN-1", tracker.EditOptions{Title: &title, Description: &desc})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KAN-1")
	assert.Contains(t, buf.String(), "New Title")
}

func TestRunListStatuses_JSON(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "To Do", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
		{Name: "Done", Category: "done"},
	}
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, key string) ([]tracker.Status, error) {
			assert.Equal(t, "KAN-1", key)
			return statuses, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListStatuses(context.Background(), p, &buf, "KAN-1", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "To Do"`)
	assert.Contains(t, buf.String(), `"type": "unstarted"`)
	assert.Contains(t, buf.String(), `"name": "Done"`)
}

func TestRunListStatuses_Table(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "To Do", Category: "unstarted"},
		{Name: "Done", Category: "done"},
	}
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
			return statuses, nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListStatuses(context.Background(), p, &buf, "KAN-1", true)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "To Do")
	assert.Contains(t, out, "Done")
}

func TestRunListStatuses_error(t *testing.T) {
	p := &mockProvider{
		listStatusesFn: func(_ context.Context, _ string) ([]tracker.Status, error) {
			return nil, fmt.Errorf("statuses failed")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunListStatuses(context.Background(), p, &buf, "KAN-1", false)
	assert.EqualError(t, err, "statuses failed")
}

func TestRunSetStatus_success(t *testing.T) {
	p := &mockProvider{
		transitionIssueFn: func(_ context.Context, key string, targetStatus string) error {
			assert.Equal(t, "KAN-1", key)
			assert.Equal(t, "Done", targetStatus)
			return nil
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunSetStatus(context.Background(), p, &buf, "KAN-1", "Done")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Transitioned KAN-1 to Done")
}

func TestRunSetStatus_error(t *testing.T) {
	p := &mockProvider{
		transitionIssueFn: func(_ context.Context, _ string, _ string) error {
			return fmt.Errorf("invalid status")
		},
	}

	var buf bytes.Buffer
	err := cmdprovider.RunSetStatus(context.Background(), p, &buf, "KAN-1", "Bogus")
	assert.EqualError(t, err, "invalid status")
	assert.Contains(t, buf.String(), "Hint: run 'human <tracker> issue statuses KAN-1'")
}

func TestPrintStatusesTable(t *testing.T) {
	statuses := []tracker.Status{
		{Name: "Open", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
		{Name: "Custom"},
	}

	var buf bytes.Buffer
	err := cmdprovider.PrintStatusesTable(&buf, statuses)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "Open")
	assert.Contains(t, out, "unstarted")
	assert.Contains(t, out, "Custom")
	assert.Contains(t, out, "-") // empty type shows as "-"
}

func TestSubcmdFromBinary(t *testing.T) {
	tests := []struct {
		arg0 string
		want string
	}{
		{"human", ""},
		{"human-browser", "browser"},
		{"/usr/local/bin/human-browser", "browser"},
		{"/usr/local/bin/human", ""},
		{"human-browser.exe", "browser"},
		{"human.exe", ""},
		{"human-daemon", "daemon"},
	}
	for _, tt := range tests {
		orig := os.Args[0]
		os.Args[0] = tt.arg0
		got := subcmdFromBinary()
		os.Args[0] = orig
		assert.Equal(t, tt.want, got, "subcmdFromBinary() with os.Args[0]=%q", tt.arg0)
	}
}

func TestRunTrackerFindWithInstances_NoMatch(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "github", Provider: &mockProvider{}},
	}

	var buf bytes.Buffer
	err := cmdtracker.RunTrackerFindWithInstances(context.Background(), &buf, "KAN-42", instances, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker matches key format")
}

// --- applyDaemonInfo tests ---

func TestApplyDaemonInfo_tokenFromInfo(t *testing.T) {
	// When caller token is empty, applyDaemonInfo should use info.Token.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:  "localhost:9999",
		Token: "info-token",
	}
	addr, token := applyDaemonInfo(info, "")
	assert.Equal(t, "localhost:9999", addr)
	assert.Equal(t, "info-token", token)
}

func TestApplyDaemonInfo_callerTokenPreserved(t *testing.T) {
	// When caller provides a token, it takes precedence over info.Token.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:  "localhost:9999",
		Token: "info-token",
	}
	addr, token := applyDaemonInfo(info, "caller-token")
	assert.Equal(t, "localhost:9999", addr)
	assert.Equal(t, "caller-token", token)
}

func TestApplyDaemonInfo_setsChromeAddr(t *testing.T) {
	// When HUMAN_CHROME_ADDR is empty and info has ChromeAddr, it should be set.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:       "localhost:9999",
		ChromeAddr: "localhost:8888",
	}
	applyDaemonInfo(info, "t")
	assert.Equal(t, "localhost:8888", os.Getenv("HUMAN_CHROME_ADDR"))
}

func TestApplyDaemonInfo_setsProxyAddr(t *testing.T) {
	// When HUMAN_PROXY_ADDR is empty and info has ProxyAddr, it should be set.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:      "localhost:9999",
		ProxyAddr: "localhost:7777",
	}
	applyDaemonInfo(info, "t")
	assert.Equal(t, "localhost:7777", os.Getenv("HUMAN_PROXY_ADDR"))
}

func TestApplyDaemonInfo_doesNotOverrideChromeAddr(t *testing.T) {
	// When HUMAN_CHROME_ADDR is already set, applyDaemonInfo should not change it.
	t.Setenv("HUMAN_CHROME_ADDR", "existing:1111")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:       "localhost:9999",
		ChromeAddr: "localhost:8888",
	}
	applyDaemonInfo(info, "t")
	assert.Equal(t, "existing:1111", os.Getenv("HUMAN_CHROME_ADDR"))
}

func TestApplyDaemonInfo_doesNotOverrideProxyAddr(t *testing.T) {
	// When HUMAN_PROXY_ADDR is already set, applyDaemonInfo should not change it.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "existing:2222")

	info := daemon.DaemonInfo{
		Addr:      "localhost:9999",
		ProxyAddr: "localhost:7777",
	}
	applyDaemonInfo(info, "t")
	assert.Equal(t, "existing:2222", os.Getenv("HUMAN_PROXY_ADDR"))
}

func TestApplyDaemonInfo_emptyInfoAddrs(t *testing.T) {
	// When info has no ChromeAddr/ProxyAddr, env vars should remain empty.
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_PROXY_ADDR", "")

	info := daemon.DaemonInfo{
		Addr:  "localhost:9999",
		Token: "tok",
	}
	addr, token := applyDaemonInfo(info, "")
	assert.Equal(t, "localhost:9999", addr)
	assert.Equal(t, "tok", token)
	assert.Equal(t, "", os.Getenv("HUMAN_CHROME_ADDR"))
	assert.Equal(t, "", os.Getenv("HUMAN_PROXY_ADDR"))
}

// --- buildHookRunE tests ---

func TestBuildHookRunE_malformedJSON(t *testing.T) {
	// Malformed JSON should be silently ignored (return nil).
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.WriteString("not valid json{{{")
	_ = w.Close()
	os.Stdin = r

	runE := buildHookRunE()
	err = runE(&cobra.Command{}, nil)
	assert.NoError(t, err)
}

func TestBuildHookRunE_emptyEventName(t *testing.T) {
	// Empty event name should be silently ignored (return nil).
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })

	input := hookInput{SessionID: "s1", Cwd: "/tmp"}
	data, _ := json.Marshal(input)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.Write(data)
	_ = w.Close()
	os.Stdin = r

	runE := buildHookRunE()
	err = runE(&cobra.Command{}, nil)
	assert.NoError(t, err)
}

func TestBuildHookRunE_noDaemon(t *testing.T) {
	// Valid input but no daemon configured should silently return nil.
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })

	// Ensure no daemon env vars or info file.
	t.Setenv("HUMAN_DAEMON_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	input := hookInput{EventName: "tool_start", SessionID: "s1", Cwd: "/tmp"}
	data, _ := json.Marshal(input)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, _ = w.Write(data)
	_ = w.Close()
	os.Stdin = r

	runE := buildHookRunE()
	err = runE(&cobra.Command{}, nil)
	assert.NoError(t, err)
}

func TestBuildHookRunE_emptyStdin(t *testing.T) {
	// Empty stdin should produce empty data, which fails json.Unmarshal
	// and is silently ignored.
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_ = w.Close()
	os.Stdin = r

	runE := buildHookRunE()
	err = runE(&cobra.Command{}, nil)
	assert.NoError(t, err)
}

// --- buildInstallCmd tests ---

func TestBuildInstallCmd_unsupportedAgent(t *testing.T) {
	cmd := buildInstallCmd()
	cmd.SetArgs([]string{"--agent", "unknown"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported agent")
}
