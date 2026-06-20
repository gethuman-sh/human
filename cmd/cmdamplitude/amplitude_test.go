package cmdamplitude

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/knowledge/amplitude"
)

// --- mock amplitude client ---

type mockAmplitudeClient struct {
	listEventsFn            func(ctx context.Context) ([]amplitude.EventType, error)
	querySegmentationFn     func(ctx context.Context, eventType, start, end, metric, interval string) (*amplitude.SegmentationResult, error)
	listTaxonomyEventsFn    func(ctx context.Context) ([]amplitude.TaxonomyEvent, error)
	listTaxonomyUserPropsFn func(ctx context.Context) ([]amplitude.TaxonomyUserProperty, error)
	queryFunnelFn           func(ctx context.Context, events []string, start, end string) (*amplitude.FunnelResult, error)
	queryRetentionFn        func(ctx context.Context, startEvent, returnEvent, start, end string) (*amplitude.RetentionResult, error)
	searchUsersFn           func(ctx context.Context, query string) ([]amplitude.UserMatch, error)
	getUserActivityFn       func(ctx context.Context, amplitudeID string) (*amplitude.UserActivity, error)
	listCohortsFn           func(ctx context.Context) ([]amplitude.Cohort, error)
}

func (m *mockAmplitudeClient) ListEvents(ctx context.Context) ([]amplitude.EventType, error) {
	return m.listEventsFn(ctx)
}

func (m *mockAmplitudeClient) QuerySegmentation(ctx context.Context, eventType, start, end, metric, interval string) (*amplitude.SegmentationResult, error) {
	return m.querySegmentationFn(ctx, eventType, start, end, metric, interval)
}

func (m *mockAmplitudeClient) ListTaxonomyEvents(ctx context.Context) ([]amplitude.TaxonomyEvent, error) {
	return m.listTaxonomyEventsFn(ctx)
}

func (m *mockAmplitudeClient) ListTaxonomyUserProperties(ctx context.Context) ([]amplitude.TaxonomyUserProperty, error) {
	return m.listTaxonomyUserPropsFn(ctx)
}

func (m *mockAmplitudeClient) QueryFunnel(ctx context.Context, events []string, start, end string) (*amplitude.FunnelResult, error) {
	return m.queryFunnelFn(ctx, events, start, end)
}

func (m *mockAmplitudeClient) QueryRetention(ctx context.Context, startEvent, returnEvent, start, end string) (*amplitude.RetentionResult, error) {
	return m.queryRetentionFn(ctx, startEvent, returnEvent, start, end)
}

func (m *mockAmplitudeClient) SearchUsers(ctx context.Context, query string) ([]amplitude.UserMatch, error) {
	return m.searchUsersFn(ctx, query)
}

func (m *mockAmplitudeClient) GetUserActivity(ctx context.Context, amplitudeID string) (*amplitude.UserActivity, error) {
	return m.getUserActivityFn(ctx, amplitudeID)
}

func (m *mockAmplitudeClient) ListCohorts(ctx context.Context) ([]amplitude.Cohort, error) {
	return m.listCohortsFn(ctx)
}

// --- events list tests ---

func TestRunAmplitudeEventsList_JSON(t *testing.T) {
	events := []amplitude.EventType{
		{Name: "page_view", TotalUsers: 5000},
		{Name: "signup", TotalUsers: 120},
	}
	client := &mockAmplitudeClient{
		listEventsFn: func(_ context.Context) ([]amplitude.EventType, error) {
			return events, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsList(context.Background(), client, &buf, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "page_view"`)
	assert.Contains(t, buf.String(), `"total_users": 5000`)
}

func TestRunAmplitudeEventsList_Table(t *testing.T) {
	events := []amplitude.EventType{
		{Name: "page_view", TotalUsers: 5000},
	}
	client := &mockAmplitudeClient{
		listEventsFn: func(_ context.Context) ([]amplitude.EventType, error) {
			return events, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsList(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "TOTAL USERS")
	assert.Contains(t, buf.String(), "page_view")
}

func TestRunAmplitudeEventsList_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		listEventsFn: func(_ context.Context) ([]amplitude.EventType, error) {
			return nil, fmt.Errorf("events failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsList(context.Background(), client, &buf, false)
	assert.EqualError(t, err, "events failed")
}

func TestRunAmplitudeEventsList_Empty(t *testing.T) {
	client := &mockAmplitudeClient{
		listEventsFn: func(_ context.Context) ([]amplitude.EventType, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsList(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No events found")
}

// --- events query tests ---

func TestRunAmplitudeEventsQuery_JSON(t *testing.T) {
	result := &amplitude.SegmentationResult{
		EventType: "page_view",
		Dates:     []string{"2026-03-01", "2026-03-02"},
		Values:    []float64{100, 200},
	}
	client := &mockAmplitudeClient{
		querySegmentationFn: func(_ context.Context, eventType, start, end, metric, interval string) (*amplitude.SegmentationResult, error) {
			assert.Equal(t, "page_view", eventType)
			assert.Equal(t, "20260301", start)
			assert.Equal(t, "20260311", end)
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsQuery(context.Background(), client, &buf, "page_view", "20260301", "20260311", "uniques", "1", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"event_type": "page_view"`)
}

func TestRunAmplitudeEventsQuery_Table(t *testing.T) {
	result := &amplitude.SegmentationResult{
		EventType: "page_view",
		Dates:     []string{"2026-03-01"},
		Values:    []float64{100},
	}
	client := &mockAmplitudeClient{
		querySegmentationFn: func(_ context.Context, _, _, _, _, _ string) (*amplitude.SegmentationResult, error) {
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsQuery(context.Background(), client, &buf, "page_view", "20260301", "20260301", "uniques", "1", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Event: page_view")
	assert.Contains(t, buf.String(), "DATE")
	assert.Contains(t, buf.String(), "2026-03-01")
}

func TestRunAmplitudeEventsQuery_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		querySegmentationFn: func(_ context.Context, _, _, _, _, _ string) (*amplitude.SegmentationResult, error) {
			return nil, fmt.Errorf("query failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeEventsQuery(context.Background(), client, &buf, "page_view", "20260301", "20260311", "", "", false)
	assert.EqualError(t, err, "query failed")
}

// --- taxonomy events tests ---

func TestRunAmplitudeTaxonomyEvents_JSON(t *testing.T) {
	events := []amplitude.TaxonomyEvent{
		{Name: "signup", Category: "Activation", Description: "User signs up"},
	}
	client := &mockAmplitudeClient{
		listTaxonomyEventsFn: func(_ context.Context) ([]amplitude.TaxonomyEvent, error) {
			return events, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyEvents(context.Background(), client, &buf, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "signup"`)
}

func TestRunAmplitudeTaxonomyEvents_Table(t *testing.T) {
	events := []amplitude.TaxonomyEvent{
		{Name: "signup", Category: "Activation", Description: "User signs up"},
	}
	client := &mockAmplitudeClient{
		listTaxonomyEventsFn: func(_ context.Context) ([]amplitude.TaxonomyEvent, error) {
			return events, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyEvents(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "CATEGORY")
	assert.Contains(t, buf.String(), "signup")
}

func TestRunAmplitudeTaxonomyEvents_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		listTaxonomyEventsFn: func(_ context.Context) ([]amplitude.TaxonomyEvent, error) {
			return nil, fmt.Errorf("taxonomy failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyEvents(context.Background(), client, &buf, false)
	assert.EqualError(t, err, "taxonomy failed")
}

func TestRunAmplitudeTaxonomyEvents_Empty(t *testing.T) {
	client := &mockAmplitudeClient{
		listTaxonomyEventsFn: func(_ context.Context) ([]amplitude.TaxonomyEvent, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyEvents(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No taxonomy events found")
}

// --- taxonomy user-properties tests ---

func TestRunAmplitudeTaxonomyUserProps_JSON(t *testing.T) {
	props := []amplitude.TaxonomyUserProperty{
		{Name: "plan", Description: "Subscription plan", Type: "string"},
	}
	client := &mockAmplitudeClient{
		listTaxonomyUserPropsFn: func(_ context.Context) ([]amplitude.TaxonomyUserProperty, error) {
			return props, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyUserProps(context.Background(), client, &buf, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "plan"`)
}

func TestRunAmplitudeTaxonomyUserProps_Table(t *testing.T) {
	props := []amplitude.TaxonomyUserProperty{
		{Name: "plan", Description: "Subscription plan", Type: "string"},
	}
	client := &mockAmplitudeClient{
		listTaxonomyUserPropsFn: func(_ context.Context) ([]amplitude.TaxonomyUserProperty, error) {
			return props, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyUserProps(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "TYPE")
	assert.Contains(t, buf.String(), "plan")
}

func TestRunAmplitudeTaxonomyUserProps_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		listTaxonomyUserPropsFn: func(_ context.Context) ([]amplitude.TaxonomyUserProperty, error) {
			return nil, fmt.Errorf("props failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyUserProps(context.Background(), client, &buf, false)
	assert.EqualError(t, err, "props failed")
}

func TestRunAmplitudeTaxonomyUserProps_Empty(t *testing.T) {
	client := &mockAmplitudeClient{
		listTaxonomyUserPropsFn: func(_ context.Context) ([]amplitude.TaxonomyUserProperty, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeTaxonomyUserProps(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No user properties found")
}

// --- funnel tests ---

func TestRunAmplitudeFunnel_JSON(t *testing.T) {
	result := &amplitude.FunnelResult{
		Steps: []amplitude.FunnelStep{
			{Name: "signup", Count: 1000, ConversionPct: 1.0},
			{Name: "purchase", Count: 200, ConversionPct: 0.2},
		},
	}
	client := &mockAmplitudeClient{
		queryFunnelFn: func(_ context.Context, events []string, start, end string) (*amplitude.FunnelResult, error) {
			assert.Equal(t, []string{"signup", "purchase"}, events)
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeFunnel(context.Background(), client, &buf, []string{"signup", "purchase"}, "20260301", "20260311", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "signup"`)
	assert.Contains(t, buf.String(), `"conversion_pct": 0.2`)
}

func TestRunAmplitudeFunnel_Table(t *testing.T) {
	result := &amplitude.FunnelResult{
		Steps: []amplitude.FunnelStep{
			{Name: "signup", Count: 1000, ConversionPct: 1.0},
		},
	}
	client := &mockAmplitudeClient{
		queryFunnelFn: func(_ context.Context, _ []string, _, _ string) (*amplitude.FunnelResult, error) {
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeFunnel(context.Background(), client, &buf, []string{"signup"}, "20260301", "20260311", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "STEP")
	assert.Contains(t, buf.String(), "EVENT")
	assert.Contains(t, buf.String(), "signup")
	assert.Contains(t, buf.String(), "100.0%")
}

func TestRunAmplitudeFunnel_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		queryFunnelFn: func(_ context.Context, _ []string, _, _ string) (*amplitude.FunnelResult, error) {
			return nil, fmt.Errorf("funnel failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeFunnel(context.Background(), client, &buf, []string{"signup"}, "20260301", "20260311", false)
	assert.EqualError(t, err, "funnel failed")
}

func TestRunAmplitudeFunnel_Empty(t *testing.T) {
	result := &amplitude.FunnelResult{Steps: nil}
	client := &mockAmplitudeClient{
		queryFunnelFn: func(_ context.Context, _ []string, _, _ string) (*amplitude.FunnelResult, error) {
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeFunnel(context.Background(), client, &buf, []string{"signup"}, "20260301", "20260311", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No funnel data")
}

// --- retention tests ---

func TestRunAmplitudeRetention_JSON(t *testing.T) {
	result := &amplitude.RetentionResult{
		StartEvent:  "signup",
		ReturnEvent: "login",
		Days: []amplitude.RetentionDay{
			{Day: 0, Count: 1000, Pct: 1.0},
			{Day: 1, Count: 400, Pct: 0.4},
		},
	}
	client := &mockAmplitudeClient{
		queryRetentionFn: func(_ context.Context, startEvent, returnEvent, start, end string) (*amplitude.RetentionResult, error) {
			assert.Equal(t, "signup", startEvent)
			assert.Equal(t, "login", returnEvent)
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeRetention(context.Background(), client, &buf, "signup", "login", "20260301", "20260311", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"start_event": "signup"`)
	assert.Contains(t, buf.String(), `"return_event": "login"`)
}

func TestRunAmplitudeRetention_Table(t *testing.T) {
	result := &amplitude.RetentionResult{
		StartEvent:  "signup",
		ReturnEvent: "login",
		Days: []amplitude.RetentionDay{
			{Day: 0, Count: 1000, Pct: 1.0},
		},
	}
	client := &mockAmplitudeClient{
		queryRetentionFn: func(_ context.Context, _, _, _, _ string) (*amplitude.RetentionResult, error) {
			return result, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeRetention(context.Background(), client, &buf, "signup", "login", "20260301", "20260311", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Start: signup")
	assert.Contains(t, buf.String(), "Return: login")
	assert.Contains(t, buf.String(), "DAY")
	assert.Contains(t, buf.String(), "RETENTION %")
}

func TestRunAmplitudeRetention_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		queryRetentionFn: func(_ context.Context, _, _, _, _ string) (*amplitude.RetentionResult, error) {
			return nil, fmt.Errorf("retention failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeRetention(context.Background(), client, &buf, "signup", "login", "20260301", "20260311", false)
	assert.EqualError(t, err, "retention failed")
}

// --- user search tests ---

func TestRunAmplitudeUserSearch_JSON(t *testing.T) {
	matches := []amplitude.UserMatch{
		{AmplitudeID: 12345, UserID: "alice@example.com", Platform: "Web", Country: "DE", LastSeen: "2026-03-10"},
	}
	client := &mockAmplitudeClient{
		searchUsersFn: func(_ context.Context, query string) ([]amplitude.UserMatch, error) {
			assert.Equal(t, "alice@example.com", query)
			return matches, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserSearch(context.Background(), client, &buf, "alice@example.com", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"user_id": "alice@example.com"`)
	assert.Contains(t, buf.String(), `"amplitude_id": 12345`)
}

func TestRunAmplitudeUserSearch_Table(t *testing.T) {
	matches := []amplitude.UserMatch{
		{AmplitudeID: 12345, UserID: "alice@example.com", Platform: "Web", Country: "DE", LastSeen: "2026-03-10"},
	}
	client := &mockAmplitudeClient{
		searchUsersFn: func(_ context.Context, _ string) ([]amplitude.UserMatch, error) {
			return matches, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserSearch(context.Background(), client, &buf, "alice@example.com", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "AMPLITUDE ID")
	assert.Contains(t, buf.String(), "USER ID")
	assert.Contains(t, buf.String(), "alice@example.com")
}

func TestRunAmplitudeUserSearch_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		searchUsersFn: func(_ context.Context, _ string) ([]amplitude.UserMatch, error) {
			return nil, fmt.Errorf("search failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserSearch(context.Background(), client, &buf, "test", false)
	assert.EqualError(t, err, "search failed")
}

func TestRunAmplitudeUserSearch_Empty(t *testing.T) {
	client := &mockAmplitudeClient{
		searchUsersFn: func(_ context.Context, _ string) ([]amplitude.UserMatch, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserSearch(context.Background(), client, &buf, "nobody", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No users found")
}

// --- user activity tests ---

func TestRunAmplitudeUserActivity_JSON(t *testing.T) {
	activity := &amplitude.UserActivity{
		AmplitudeID: 12345,
		Events: []amplitude.ActivityEvent{
			{Type: "page_view", Time: "2026-03-10T12:00:00Z", Properties: map[string]any{"page": "/home"}},
		},
	}
	client := &mockAmplitudeClient{
		getUserActivityFn: func(_ context.Context, amplitudeID string) (*amplitude.UserActivity, error) {
			assert.Equal(t, "12345", amplitudeID)
			return activity, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserActivity(context.Background(), client, &buf, "12345", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"amplitude_id": 12345`)
	assert.Contains(t, buf.String(), `"type": "page_view"`)
}

func TestRunAmplitudeUserActivity_Table(t *testing.T) {
	activity := &amplitude.UserActivity{
		AmplitudeID: 12345,
		Events: []amplitude.ActivityEvent{
			{Type: "page_view", Time: "2026-03-10T12:00:00Z"},
		},
	}
	client := &mockAmplitudeClient{
		getUserActivityFn: func(_ context.Context, _ string) (*amplitude.UserActivity, error) {
			return activity, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserActivity(context.Background(), client, &buf, "12345", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Amplitude ID: 12345")
	assert.Contains(t, buf.String(), "TYPE")
	assert.Contains(t, buf.String(), "page_view")
}

func TestRunAmplitudeUserActivity_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		getUserActivityFn: func(_ context.Context, _ string) (*amplitude.UserActivity, error) {
			return nil, fmt.Errorf("activity failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeUserActivity(context.Background(), client, &buf, "12345", false)
	assert.EqualError(t, err, "activity failed")
}

// --- cohorts list tests ---

func TestRunAmplitudeCohortsList_JSON(t *testing.T) {
	size := 1500
	cohorts := []amplitude.Cohort{
		{ID: "abc123", Name: "Power Users", Description: "Active daily", Size: &size, Archived: false},
	}
	client := &mockAmplitudeClient{
		listCohortsFn: func(_ context.Context) ([]amplitude.Cohort, error) {
			return cohorts, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeCohortsList(context.Background(), client, &buf, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "Power Users"`)
	assert.Contains(t, buf.String(), `"size": 1500`)
}

func TestRunAmplitudeCohortsList_Table(t *testing.T) {
	size := 1500
	cohorts := []amplitude.Cohort{
		{ID: "abc123", Name: "Power Users", Description: "Active daily", Size: &size, Archived: false},
	}
	client := &mockAmplitudeClient{
		listCohortsFn: func(_ context.Context) ([]amplitude.Cohort, error) {
			return cohorts, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeCohortsList(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Power Users")
	assert.Contains(t, buf.String(), "1500")
}

func TestRunAmplitudeCohortsList_Error(t *testing.T) {
	client := &mockAmplitudeClient{
		listCohortsFn: func(_ context.Context) ([]amplitude.Cohort, error) {
			return nil, fmt.Errorf("cohorts failed")
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeCohortsList(context.Background(), client, &buf, false)
	assert.EqualError(t, err, "cohorts failed")
}

func TestRunAmplitudeCohortsList_Empty(t *testing.T) {
	client := &mockAmplitudeClient{
		listCohortsFn: func(_ context.Context) ([]amplitude.Cohort, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runAmplitudeCohortsList(context.Background(), client, &buf, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No cohorts found")
}

// --- table empty state tests ---

func TestPrintAmplitudeEventsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printAmplitudeEventsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No events found")
}

func TestPrintAmplitudeTaxonomyEventsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printAmplitudeTaxonomyEventsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No taxonomy events found")
}

func TestPrintAmplitudeTaxonomyUserPropsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printAmplitudeTaxonomyUserPropsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No user properties found")
}

func TestPrintAmplitudeUserSearchTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printAmplitudeUserSearchTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No users found")
}

func TestPrintAmplitudeCohortsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printAmplitudeCohortsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No cohorts found")
}

// --- command tree tests ---

func TestAmplitudeCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAmplitudeCommands()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["events"], "expected 'events' subcommand")
	assert.True(t, subNames["taxonomy"], "expected 'taxonomy' subcommand")
	assert.True(t, subNames["funnel"], "expected 'funnel' subcommand")
	assert.True(t, subNames["retention"], "expected 'retention' subcommand")
	assert.True(t, subNames["user"], "expected 'user' subcommand")
	assert.True(t, subNames["cohorts"], "expected 'cohorts' subcommand")
}

func TestAmplitudeEventsCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAmplitudeCommands()
	var eventsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "events" {
			eventsCmd = sub
			break
		}
	}
	require.NotNil(t, eventsCmd)

	subNames := make(map[string]bool)
	for _, sub := range eventsCmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["list"], "expected 'list' subcommand")
	assert.True(t, subNames["query"], "expected 'query' subcommand")
}

func TestAmplitudeTaxonomyCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAmplitudeCommands()
	var taxCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "taxonomy" {
			taxCmd = sub
			break
		}
	}
	require.NotNil(t, taxCmd)

	subNames := make(map[string]bool)
	for _, sub := range taxCmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["events"], "expected 'events' subcommand")
	assert.True(t, subNames["user-properties"], "expected 'user-properties' subcommand")
}

func TestAmplitudeUserCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAmplitudeCommands()
	var userCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "user" {
			userCmd = sub
			break
		}
	}
	require.NotNil(t, userCmd)

	subNames := make(map[string]bool)
	for _, sub := range userCmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["search QUERY"], "expected 'search' subcommand")
	assert.True(t, subNames["activity AMPLITUDE_ID"], "expected 'activity' subcommand")
}

func TestAmplitudeCohortsCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAmplitudeCommands()
	var cohortsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "cohorts" {
			cohortsCmd = sub
			break
		}
	}
	require.NotNil(t, cohortsCmd)

	subNames := make(map[string]bool)
	for _, sub := range cohortsCmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["list"], "expected 'list' subcommand")
}

// --- segmentation table edge case ---

func TestPrintAmplitudeSegmentationTable_noData(t *testing.T) {
	var buf bytes.Buffer
	result := &amplitude.SegmentationResult{
		EventType: "test",
		Dates:     nil,
		Values:    nil,
	}
	err := printAmplitudeSegmentationTable(&buf, result)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Event: test")
	assert.Contains(t, buf.String(), "No data")
}

// --- retention table edge case ---

func TestPrintAmplitudeRetentionTable_noData(t *testing.T) {
	var buf bytes.Buffer
	result := &amplitude.RetentionResult{
		StartEvent:  "signup",
		ReturnEvent: "login",
		Days:        nil,
	}
	err := printAmplitudeRetentionTable(&buf, result)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Start: signup")
	assert.Contains(t, buf.String(), "No retention data")
}

// --- user activity table edge case ---

func TestPrintAmplitudeUserActivityTable_noEvents(t *testing.T) {
	var buf bytes.Buffer
	activity := &amplitude.UserActivity{
		AmplitudeID: 12345,
		Events:      nil,
	}
	err := printAmplitudeUserActivityTable(&buf, activity)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Amplitude ID: 12345")
	assert.Contains(t, buf.String(), "No events found")
}

// --- cohorts table with nil size ---

func TestPrintAmplitudeCohortsTable_nilSize(t *testing.T) {
	var buf bytes.Buffer
	cohorts := []amplitude.Cohort{
		{ID: "abc", Name: "Test", Description: "Desc", Size: nil, Archived: true},
	}
	err := printAmplitudeCohortsTable(&buf, cohorts)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "-")
	assert.Contains(t, buf.String(), "yes")
}
