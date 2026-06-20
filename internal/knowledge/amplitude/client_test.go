package amplitude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequest_setsBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "my-api-key", user)
		assert.Equal(t, "my-secret-key", pass)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "my-api-key", "my-secret-key")
	resp, err := client.doRequest(context.Background(), http.MethodGet, "/api/2/events/list", "")
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDoRequest_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"Invalid API key"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "bad-key", "bad-secret")
	_, err := client.doRequest(context.Background(), http.MethodGet, "/api/2/events/list", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 401")
}

func TestListEvents_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/2/events/list", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"data": [
				{"name": "page_view", "totals": 5000},
				{"name": "signup", "totals": 120}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	events, err := client.ListEvents(context.Background())
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "page_view", events[0].Name)
	assert.Equal(t, 5000, events[0].TotalUsers)
	assert.Equal(t, "signup", events[1].Name)
	assert.Equal(t, 120, events[1].TotalUsers)
}

func TestListEvents_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	_, err := client.ListEvents(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 403")
}

func TestQuerySegmentation_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/events/segmentation", r.URL.Path)
		assert.Contains(t, r.URL.RawQuery, "start=20260301")
		assert.Contains(t, r.URL.RawQuery, "end=20260311")
		_, _ = fmt.Fprint(w, `{
			"data": {
				"series": [[100, 200, 150]],
				"seriesLabels": [["page_view"]],
				"xValues": ["2026-03-01", "2026-03-02", "2026-03-03"]
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	result, err := client.QuerySegmentation(context.Background(), "page_view", "20260301", "20260311", "uniques", "1")
	require.NoError(t, err)
	assert.Equal(t, "page_view", result.EventType)
	assert.Equal(t, []string{"2026-03-01", "2026-03-02", "2026-03-03"}, result.Dates)
	assert.Equal(t, []float64{100, 200, 150}, result.Values)
}

func TestQuerySegmentation_emptySeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"data": {
				"series": [],
				"seriesLabels": [],
				"xValues": ["2026-03-01"]
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	result, err := client.QuerySegmentation(context.Background(), "_active", "20260301", "20260301", "", "")
	require.NoError(t, err)
	assert.Nil(t, result.Values)
}

func TestListTaxonomyEvents_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/taxonomy/event", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"success": true,
			"data": [
				{"event_type": "signup", "category": "Activation", "description": "User signs up"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	events, err := client.ListTaxonomyEvents(context.Background())
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "signup", events[0].Name)
	assert.Equal(t, "Activation", events[0].Category)
	assert.Equal(t, "User signs up", events[0].Description)
}

func TestListTaxonomyUserProperties_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/taxonomy/user-property", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"success": true,
			"data": [
				{"user_property": "plan", "description": "Subscription plan", "type": "string"}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	props, err := client.ListTaxonomyUserProperties(context.Background())
	require.NoError(t, err)
	require.Len(t, props, 1)
	assert.Equal(t, "plan", props[0].Name)
	assert.Equal(t, "Subscription plan", props[0].Description)
	assert.Equal(t, "string", props[0].Type)
}

func TestQueryFunnel_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/funnels", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"data": {
				"steps": [
					{"event": "signup", "count": 1000, "step_conv_ratio": 1.0},
					{"event": "purchase", "count": 200, "step_conv_ratio": 0.2}
				]
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	result, err := client.QueryFunnel(context.Background(), []string{"signup", "purchase"}, "20260301", "20260311")
	require.NoError(t, err)
	require.Len(t, result.Steps, 2)
	assert.Equal(t, "signup", result.Steps[0].Name)
	assert.Equal(t, 1000, result.Steps[0].Count)
	assert.Equal(t, 1.0, result.Steps[0].ConversionPct)
	assert.Equal(t, "purchase", result.Steps[1].Name)
	assert.Equal(t, 0.2, result.Steps[1].ConversionPct)
}

func TestQueryRetention_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/retention", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"data": {
				"counts": [
					{"day": 0, "count": 1000, "percentage": 1.0},
					{"day": 1, "count": 400, "percentage": 0.4},
					{"day": 7, "count": 200, "percentage": 0.2}
				]
			}
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	result, err := client.QueryRetention(context.Background(), "signup", "login", "20260301", "20260311")
	require.NoError(t, err)
	assert.Equal(t, "signup", result.StartEvent)
	assert.Equal(t, "login", result.ReturnEvent)
	require.Len(t, result.Days, 3)
	assert.Equal(t, 0, result.Days[0].Day)
	assert.Equal(t, 1000, result.Days[0].Count)
	assert.Equal(t, 7, result.Days[2].Day)
	assert.Equal(t, 0.2, result.Days[2].Pct)
}

func TestSearchUsers_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/usersearch", r.URL.Path)
		assert.Equal(t, "alice@example.com", r.URL.Query().Get("user"))
		_, _ = fmt.Fprint(w, `{
			"matches": [
				{
					"amplitude_id": 12345,
					"user_id": "alice@example.com",
					"platform": "Web",
					"country": "DE",
					"last_seen": "2026-03-10T12:00:00Z"
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	matches, err := client.SearchUsers(context.Background(), "alice@example.com")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, int64(12345), matches[0].AmplitudeID)
	assert.Equal(t, "alice@example.com", matches[0].UserID)
	assert.Equal(t, "Web", matches[0].Platform)
	assert.Equal(t, "DE", matches[0].Country)
}

func TestGetUserActivity_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/2/useractivity", r.URL.Path)
		assert.Equal(t, "12345", r.URL.Query().Get("user"))
		_, _ = fmt.Fprint(w, `{
			"userData": {"amplitude_id": 12345},
			"events": [
				{
					"event_type": "page_view",
					"event_time": "2026-03-10T12:00:00Z",
					"event_properties": {"page": "/home"}
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	activity, err := client.GetUserActivity(context.Background(), "12345")
	require.NoError(t, err)
	assert.Equal(t, int64(12345), activity.AmplitudeID)
	require.Len(t, activity.Events, 1)
	assert.Equal(t, "page_view", activity.Events[0].Type)
	assert.Equal(t, "/home", activity.Events[0].Properties["page"])
}

func TestListCohorts_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/3/cohorts", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"cohorts": [
				{
					"id": "abc123",
					"name": "Power Users",
					"description": "Users who visit 5+ times per week",
					"size": 1500,
					"archived": false
				},
				{
					"id": "def456",
					"name": "Churned",
					"description": "No activity in 30 days",
					"size": null,
					"archived": true
				}
			]
		}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	cohorts, err := client.ListCohorts(context.Background())
	require.NoError(t, err)
	require.Len(t, cohorts, 2)
	assert.Equal(t, "abc123", cohorts[0].ID)
	assert.Equal(t, "Power Users", cohorts[0].Name)
	require.NotNil(t, cohorts[0].Size)
	assert.Equal(t, 1500, *cohorts[0].Size)
	assert.False(t, cohorts[0].Archived)
	assert.Equal(t, "def456", cohorts[1].ID)
	assert.Nil(t, cohorts[1].Size)
	assert.True(t, cohorts[1].Archived)
}

func TestListCohorts_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"cohorts": []}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "key", "secret")
	cohorts, err := client.ListCohorts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, cohorts)
}

func TestBuildEventJSON(t *testing.T) {
	assert.Equal(t, `{"event_type":"signup"}`, buildEventJSON("signup"))
	assert.Equal(t, `{"event_type":"_active"}`, buildEventJSON("_active"))
}

func TestBuildFunnelEventsJSON(t *testing.T) {
	result := buildFunnelEventsJSON([]string{"signup", "purchase"})
	assert.Equal(t, `[{"event_type":"signup"},{"event_type":"purchase"}]`, result)
}

func TestBuildFunnelEventsJSON_single(t *testing.T) {
	result := buildFunnelEventsJSON([]string{"signup"})
	assert.Equal(t, `[{"event_type":"signup"}]`, result)
}
