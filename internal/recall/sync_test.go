package recall

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// mockProvider implements tracker.Provider for testing.
type mockProvider struct {
	listFn func(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error)
	getFn  func(ctx context.Context, key string) (*tracker.Issue, error)
}

func (m *mockProvider) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	return m.listFn(ctx, opts)
}

func (m *mockProvider) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	return m.getFn(ctx, key)
}

func (m *mockProvider) CreateIssue(_ context.Context, _ *tracker.Issue) (*tracker.Issue, error) {
	return nil, nil
}

func (m *mockProvider) ListComments(_ context.Context, _ string) ([]tracker.Comment, error) {
	return nil, nil
}

func (m *mockProvider) AddComment(_ context.Context, _ string, _ string) (*tracker.Comment, error) {
	return nil, nil
}

func (m *mockProvider) LinkIssues(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockProvider) DeleteIssue(_ context.Context, _ string) error {
	return nil
}

func (m *mockProvider) TransitionIssue(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockProvider) AssignIssue(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockProvider) GetCurrentUser(_ context.Context) (string, error) {
	return "", nil
}

func (m *mockProvider) EditIssue(_ context.Context, _ string, _ tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}

func (m *mockProvider) ListStatuses(_ context.Context, _ string) ([]tracker.Status, error) {
	return nil, nil
}

func TestSync_indexesIssues(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{
				{Key: opts.Project + "-1", Title: "Issue one"},
				{Key: opts.Project + "-2", Title: "Issue two"},
			}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Title for " + key, Description: "Desc for " + key, Status: "Open"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com", Projects: []string{"KAN"}, Provider: provider},
	}

	result, err := Sync(ctx, s, instances, false, &buf)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Indexed != 2 {
		t.Errorf("expected 2 indexed, got %d", result.Indexed)
	}
	if result.Errors != 0 {
		t.Errorf("expected 0 errors, got %d", result.Errors)
	}

	keys, _ := s.AllKeys(ctx, "work")
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestSync_prunesStaleEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate a stale entry.
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-99", Source: "work", Kind: "jira"}, "old"))

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Current"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	// Use fullSync=true to trigger pruning (incremental sync skips pruning).
	result, _ := Sync(ctx, s, instances, true, &buf)
	if result.Pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", result.Pruned)
	}

	keys, _ := s.AllKeys(ctx, "work")
	if len(keys) != 1 || keys[0] != "KAN-1" {
		t.Errorf("expected [KAN-1], got %v", keys)
	}
}

func TestSync_allProjectsWhenNoneConfigured(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			if opts.Project != "" {
				t.Errorf("expected empty project for all-projects sync, got %q", opts.Project)
			}
			return []tracker.Issue{{Key: "KAN-1", Project: "KAN"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Cross-project", Project: "KAN"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Provider: provider},
	}

	result, err := Sync(ctx, s, instances, false, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed, got %d", result.Indexed)
	}
}

func TestSync_handlesListError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	errorProvider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return nil, context.DeadlineExceeded
		},
	}
	okProvider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "ENG-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "OK"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "broken", Kind: "jira", Projects: []string{"BAD"}, Provider: errorProvider},
		{Name: "working", Kind: "linear", Projects: []string{"ENG"}, Provider: okProvider},
	}

	result, _ := Sync(ctx, s, instances, false, &buf)
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed from working instance, got %d", result.Indexed)
	}
}

func TestSync_handlesGetError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}, {Key: "KAN-2"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			if key == "KAN-1" {
				return nil, context.DeadlineExceeded
			}
			return &tracker.Issue{Key: key, Title: "OK"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	result, _ := Sync(ctx, s, instances, false, &buf)
	if result.Errors != 1 {
		t.Errorf("expected 1 error (KAN-1 fetch), got %d", result.Errors)
	}
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed (KAN-2), got %d", result.Indexed)
	}
}

func TestSync_emptyInstances(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	result, err := Sync(ctx, s, nil, false, &buf)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Indexed != 0 || result.Pruned != 0 || result.Errors != 0 {
		t.Errorf("expected all zeros, got %+v", result)
	}
}

func TestSync_incrementalPassesUpdatedSince(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate an entry so LastIndexedAt returns non-zero.
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-1", Source: "work", Kind: "jira", Title: "Old"}, "old desc"))

	var capturedOpts tracker.ListOptions
	provider := &mockProvider{
		listFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			capturedOpts = opts
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Updated"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	result, err := Sync(ctx, s, instances, false, &buf)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// UpdatedSince should be set on incremental run.
	if capturedOpts.UpdatedSince.IsZero() {
		t.Error("expected UpdatedSince to be non-zero on incremental sync")
	}
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed, got %d", result.Indexed)
	}
	// Incremental sync should not prune.
	if result.Pruned != 0 {
		t.Errorf("expected 0 pruned on incremental sync, got %d", result.Pruned)
	}

	// Verify log mentions incremental.
	if !bytes.Contains(buf.Bytes(), []byte("Incremental sync")) {
		t.Errorf("expected 'Incremental sync' in log, got:\n%s", buf.String())
	}
}

func TestSync_fullSyncWhenNoExistingEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	var capturedOpts tracker.ListOptions
	provider := &mockProvider{
		listFn: func(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
			capturedOpts = opts
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "New"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	_, err := Sync(ctx, s, instances, false, &buf)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// First sync should be full (UpdatedSince zero).
	if !capturedOpts.UpdatedSince.IsZero() {
		t.Error("expected UpdatedSince to be zero on first sync")
	}

	if !bytes.Contains(buf.Bytes(), []byte("Full sync")) {
		t.Errorf("expected 'Full sync' in log, got:\n%s", buf.String())
	}
}

func TestSync_fullFlagForcesPruning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate entries.
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-1", Source: "work", Kind: "jira"}, "d"))
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-99", Source: "work", Kind: "jira"}, "stale"))

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Current"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	// fullSync=true should prune even though entries exist.
	result, _ := Sync(ctx, s, instances, true, &buf)
	if result.Pruned != 1 {
		t.Errorf("expected 1 pruned with --full, got %d", result.Pruned)
	}
	if !bytes.Contains(buf.Bytes(), []byte("Full sync")) {
		t.Errorf("expected 'Full sync' in log, got:\n%s", buf.String())
	}
}

func TestSync_incrementalSkipsPruning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate entries including a "stale" one.
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-1", Source: "work", Kind: "jira"}, "d"))
	require.NoError(t, s.UpsertEntry(ctx, Entry{Key: "KAN-99", Source: "work", Kind: "jira"}, "stale"))

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			// Only returns KAN-1, not KAN-99.
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Current"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	// Incremental sync should NOT prune KAN-99.
	result, _ := Sync(ctx, s, instances, false, &buf)
	if result.Pruned != 0 {
		t.Errorf("expected 0 pruned on incremental sync, got %d", result.Pruned)
	}

	keys, _ := s.AllKeys(ctx, "work")
	if len(keys) != 2 {
		t.Errorf("expected 2 keys (no pruning), got %v", keys)
	}
}

// TestSync_transientFetchErrorDoesNotPrune verifies the M11.1 invariant:
// when a per-issue fetch fails transiently during a full sync, the key
// must NOT be deleted from the index on the subsequent prune pass. The
// upstream issue still exists and will be re-indexed on the next run.
func TestSync_transientFetchErrorDoesNotPrune(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	// Pre-populate the store with an entry that should survive a
	// failing fetch on the next sync.
	require.NoError(t, s.UpsertEntry(ctx, Entry{
		Key: "KAN-1", Source: "work", Kind: "jira", Project: "KAN",
		Title: "Old title", URL: "https://jira.example.com",
	}, "old description"))

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			// List still reports the issue as existing upstream.
			return []tracker.Issue{{Key: "KAN-1", Title: "Still here"}}, nil
		},
		getFn: func(_ context.Context, _ string) (*tracker.Issue, error) {
			// But the per-issue fetch fails — e.g. rate limit, flap.
			return nil, context.DeadlineExceeded
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com", Projects: []string{"KAN"}, Provider: provider},
	}

	result, err := Sync(ctx, s, instances, true, &buf)
	require.NoError(t, err)
	// We expect 1 error (the failed fetch) and 0 prunes — the listed
	// key must have been added to `seen` before the fetch attempt.
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
	if result.Pruned != 0 {
		t.Errorf("expected 0 pruned (transient fetch error must not drop entries), got %d", result.Pruned)
	}

	keys, _ := s.AllKeys(ctx, "work")
	if len(keys) != 1 {
		t.Errorf("expected 1 key to survive, got %v", keys)
	}
}

func TestSync_usesIssueProjectWhenEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			return &tracker.Issue{Key: key, Title: "Cross-project", Project: "PROJ"}, nil
		},
	}

	// No projects configured - project should come from issue.
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Provider: provider},
	}

	result, err := Sync(ctx, s, instances, false, &buf)
	require.NoError(t, err)
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed, got %d", result.Indexed)
	}

	results, _ := s.Search(ctx, "Cross-project", 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Project != "PROJ" {
		t.Errorf("Project = %q, want PROJ", results[0].Project)
	}
}

// TestSync_nilIssueFromProvider verifies that a provider returning (nil, nil)
// from GetIssue does not crash the sync and is treated as a skip.
func TestSync_nilIssueFromProvider(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{{Key: "KAN-1"}, {Key: "KAN-2"}}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			if key == "KAN-1" {
				return nil, nil // provider returns nil issue
			}
			return &tracker.Issue{Key: key, Title: "OK"}, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", Projects: []string{"KAN"}, Provider: provider},
	}

	result, _ := Sync(ctx, s, instances, false, &buf)
	// KAN-1 should be skipped (nil issue), KAN-2 should be indexed.
	if result.Indexed != 1 {
		t.Errorf("expected 1 indexed (KAN-2), got %d", result.Indexed)
	}

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("Skipping KAN-1")) {
		t.Errorf("expected skip message for nil issue, got:\n%s", logOutput)
	}
}

// TestSync_preferFullIssueURL verifies the M11.2 fix: entry.URL is
// populated from the per-issue web URL when the provider sets it,
// instead of always being the instance base URL.
func TestSync_preferFullIssueURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var buf bytes.Buffer

	provider := &mockProvider{
		listFn: func(_ context.Context, _ tracker.ListOptions) ([]tracker.Issue, error) {
			return []tracker.Issue{
				{Key: "KAN-1", Title: "With URL"},
				{Key: "KAN-2", Title: "No URL"},
			}, nil
		},
		getFn: func(_ context.Context, key string) (*tracker.Issue, error) {
			iss := &tracker.Issue{Key: key, Title: "Title " + key, Status: "Open"}
			if key == "KAN-1" {
				iss.URL = "https://jira.example.com/browse/KAN-1"
			}
			return iss, nil
		},
	}

	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com", Projects: []string{"KAN"}, Provider: provider},
	}

	_, err := Sync(ctx, s, instances, true, &buf)
	require.NoError(t, err)

	got, err := s.Search(ctx, "Title", 10)
	require.NoError(t, err)
	urls := map[string]string{}
	for _, e := range got {
		urls[e.Key] = e.URL
	}
	if urls["KAN-1"] != "https://jira.example.com/browse/KAN-1" {
		t.Errorf("KAN-1 URL should be per-issue, got %q", urls["KAN-1"])
	}
	if urls["KAN-2"] != "https://jira.example.com" {
		t.Errorf("KAN-2 URL should fall back to instance URL, got %q", urls["KAN-2"])
	}
}
