package amplitude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/gethuman-sh/human/internal/apiclient"
)

// Client is an Amplitude API client.
type Client struct {
	api *apiclient.Client
}

// New creates an Amplitude client with the given base URL, API key, and secret key.
func New(baseURL, apiKey, secretKey string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BasicAuth(apiKey, secretKey)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithProviderName("amplitude"),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListEvents fetches all event types with WAU counts.
func (c *Client) ListEvents(ctx context.Context) ([]EventType, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/events/list", "")
	if err != nil {
		return nil, err
	}
	var r eventsListResponse
	if err := apiclient.DecodeJSON(resp, &r); err != nil {
		return nil, err
	}

	events := make([]EventType, len(r.Data))
	for i, e := range r.Data {
		events[i] = EventType{Name: e.Name, TotalUsers: e.Totals}
	}
	return events, nil
}

// QuerySegmentation runs a segmentation query for an event type.
func (c *Client) QuerySegmentation(ctx context.Context, eventType, start, end, metric, interval string) (*SegmentationResult, error) {
	q := url.Values{}
	q.Set("e", buildEventJSON(eventType))
	q.Set("start", start)
	q.Set("end", end)
	if metric != "" {
		q.Set("m", metric)
	}
	if interval != "" {
		q.Set("i", interval)
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/events/segmentation", q.Encode())
	if err != nil {
		return nil, err
	}
	var r segmentationResponse
	if err := apiclient.DecodeJSON(resp, &r, "eventType", eventType); err != nil {
		return nil, err
	}

	result := &SegmentationResult{
		EventType: eventType,
		Dates:     r.Data.XValues,
	}
	if len(r.Data.Series) > 0 {
		result.Values = r.Data.Series[0]
	}
	return result, nil
}

// ListTaxonomyEvents fetches the event type schema.
func (c *Client) ListTaxonomyEvents(ctx context.Context) ([]TaxonomyEvent, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/taxonomy/event", "")
	if err != nil {
		return nil, err
	}
	var r taxonomyResponse
	if err := apiclient.DecodeJSON(resp, &r); err != nil {
		return nil, err
	}

	events := make([]TaxonomyEvent, len(r.Data))
	for i, e := range r.Data {
		events[i] = TaxonomyEvent{
			Name:        e.EventType,
			Category:    e.Category,
			Description: e.Description,
		}
	}
	return events, nil
}

// ListTaxonomyUserProperties fetches the user property schema.
func (c *Client) ListTaxonomyUserProperties(ctx context.Context) ([]TaxonomyUserProperty, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/taxonomy/user-property", "")
	if err != nil {
		return nil, err
	}
	var r taxonomyUserPropResponse
	if err := apiclient.DecodeJSON(resp, &r); err != nil {
		return nil, err
	}

	props := make([]TaxonomyUserProperty, len(r.Data))
	for i, p := range r.Data {
		props[i] = TaxonomyUserProperty{
			Name:        p.UserProperty,
			Description: p.Description,
			Type:        p.Type,
		}
	}
	return props, nil
}

// QueryFunnel runs a funnel analysis for a sequence of events.
func (c *Client) QueryFunnel(ctx context.Context, events []string, start, end string) (*FunnelResult, error) {
	q := url.Values{}
	q.Set("e", buildFunnelEventsJSON(events))
	q.Set("start", start)
	q.Set("end", end)

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/funnels", q.Encode())
	if err != nil {
		return nil, err
	}
	var r funnelResponse
	if err := apiclient.DecodeJSON(resp, &r); err != nil {
		return nil, err
	}

	result := &FunnelResult{
		Steps: make([]FunnelStep, len(r.Data.Steps)),
	}
	for i, s := range r.Data.Steps {
		result.Steps[i] = FunnelStep{
			Name:          s.EventName,
			Count:         s.StepCount,
			ConversionPct: s.ConversionPct,
		}
	}
	return result, nil
}

// QueryRetention runs a retention analysis.
func (c *Client) QueryRetention(ctx context.Context, startEvent, returnEvent, start, end string) (*RetentionResult, error) {
	q := url.Values{}
	q.Set("se", buildEventJSON(startEvent))
	q.Set("re", buildEventJSON(returnEvent))
	q.Set("start", start)
	q.Set("end", end)

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/retention", q.Encode())
	if err != nil {
		return nil, err
	}
	var r retentionResponse
	if err := apiclient.DecodeJSON(resp, &r, "startEvent", startEvent, "returnEvent", returnEvent); err != nil {
		return nil, err
	}

	result := &RetentionResult{
		StartEvent:  startEvent,
		ReturnEvent: returnEvent,
		Days:        make([]RetentionDay, len(r.Data.Counts)),
	}
	for i, c := range r.Data.Counts {
		result.Days[i] = RetentionDay(c)
	}
	return result, nil
}

// SearchUsers searches for users by query string.
func (c *Client) SearchUsers(ctx context.Context, query string) ([]UserMatch, error) {
	q := url.Values{}
	q.Set("user", query)

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/usersearch", q.Encode())
	if err != nil {
		return nil, err
	}
	var r userSearchResponse
	if err := apiclient.DecodeJSON(resp, &r, "query", query); err != nil {
		return nil, err
	}

	matches := make([]UserMatch, len(r.Matches))
	for i, m := range r.Matches {
		matches[i] = UserMatch(m)
	}
	return matches, nil
}

// GetUserActivity fetches a user's event history by Amplitude ID.
func (c *Client) GetUserActivity(ctx context.Context, amplitudeID string) (*UserActivity, error) {
	q := url.Values{}
	q.Set("user", amplitudeID)

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/2/useractivity", q.Encode())
	if err != nil {
		return nil, err
	}
	var r userActivityResponse
	if err := apiclient.DecodeJSON(resp, &r, "amplitudeID", amplitudeID); err != nil {
		return nil, err
	}

	activity := &UserActivity{
		AmplitudeID: r.UserData.AmplitudeID,
		Events:      make([]ActivityEvent, len(r.Events)),
	}
	for i, e := range r.Events {
		activity.Events[i] = ActivityEvent{
			Type:       e.EventType,
			Time:       e.EventTime,
			Properties: e.EventProperties,
		}
	}
	return activity, nil
}

// ListCohorts fetches all behavioral cohorts.
func (c *Client) ListCohorts(ctx context.Context) ([]Cohort, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/3/cohorts", "")
	if err != nil {
		return nil, err
	}
	var r cohortsResponse
	if err := apiclient.DecodeJSON(resp, &r); err != nil {
		return nil, err
	}

	cohorts := make([]Cohort, len(r.Cohorts))
	for i, c := range r.Cohorts {
		cohorts[i] = Cohort(c)
	}
	return cohorts, nil
}

// doRequest performs an authenticated HTTP request to the Amplitude API.
func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string) (*http.Response, error) {
	return c.api.Do(ctx, method, path, rawQuery, nil)
}

// eventTypeObj is the JSON shape for an Amplitude event type parameter.
type eventTypeObj struct {
	EventType string `json:"event_type"`
}

// buildEventJSON returns a JSON-encoded event type object for query parameters.
func buildEventJSON(eventType string) string {
	b, _ := json.Marshal(eventTypeObj{EventType: eventType})
	return string(b)
}

// buildFunnelEventsJSON returns a JSON array of event type objects for funnel query parameters.
func buildFunnelEventsJSON(events []string) string {
	objs := make([]eventTypeObj, len(events))
	for i, e := range events {
		objs[i] = eventTypeObj{EventType: e}
	}
	b, _ := json.Marshal(objs)
	return string(b)
}
