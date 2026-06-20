package amplitude

// --- Private API types (JSON deserialization) ---

// eventsListResponse is the response from GET /api/2/events/list.
type eventsListResponse struct {
	Data []eventData `json:"data"`
}

// eventData represents a single event type from events/list.
type eventData struct {
	Name   string `json:"name"`
	Totals int    `json:"totals"`
}

// segmentationResponse is the response from GET /api/2/events/segmentation.
type segmentationResponse struct {
	Data segmentationData `json:"data"`
}

// segmentationData holds segmentation series data.
type segmentationData struct {
	Series       [][]float64 `json:"series"`
	SeriesLabels [][]string  `json:"seriesLabels"`
	XValues      []string    `json:"xValues"`
}

// taxonomyResponse is the response from GET /api/2/taxonomy/event.
type taxonomyResponse struct {
	Success bool            `json:"success"`
	Data    []taxonomyEvent `json:"data"`
}

// taxonomyEvent represents an event type in the taxonomy.
type taxonomyEvent struct {
	EventType   string `json:"event_type"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// taxonomyUserPropResponse is the response from GET /api/2/taxonomy/user-property.
type taxonomyUserPropResponse struct {
	Success bool               `json:"success"`
	Data    []taxonomyUserProp `json:"data"`
}

// taxonomyUserProp represents a user property in the taxonomy.
type taxonomyUserProp struct {
	UserProperty string `json:"user_property"`
	Description  string `json:"description"`
	Type         string `json:"type"`
}

// funnelResponse is the response from GET /api/2/funnels.
type funnelResponse struct {
	Data funnelData `json:"data"`
}

// funnelData holds the funnel steps.
type funnelData struct {
	Steps []funnelStep `json:"steps"`
}

// funnelStep represents a step in a funnel.
type funnelStep struct {
	EventName     string  `json:"event"`
	StepCount     int     `json:"count"`
	ConversionPct float64 `json:"step_conv_ratio"`
}

// retentionResponse is the response from GET /api/2/retention.
type retentionResponse struct {
	Data retentionData `json:"data"`
}

// retentionData holds the retention curve.
type retentionData struct {
	Counts []retentionCount `json:"counts"`
}

// retentionCount represents a single day's retention data.
type retentionCount struct {
	Day   int     `json:"day"`
	Count int     `json:"count"`
	Pct   float64 `json:"percentage"`
}

// userSearchResponse is the response from GET /api/2/usersearch.
type userSearchResponse struct {
	Matches []userMatch `json:"matches"`
}

// userMatch represents a user match from the search API.
type userMatch struct {
	AmplitudeID int64  `json:"amplitude_id"`
	UserID      string `json:"user_id"`
	Platform    string `json:"platform"`
	Country     string `json:"country"`
	LastSeen    string `json:"last_seen"`
}

// userActivityResponse is the response from GET /api/2/useractivity.
type userActivityResponse struct {
	UserData userData        `json:"userData"`
	Events   []activityEvent `json:"events"`
}

// userData holds user metadata.
type userData struct {
	AmplitudeID int64 `json:"amplitude_id"`
}

// activityEvent represents a single event in a user's activity history.
type activityEvent struct {
	EventType       string         `json:"event_type"`
	EventTime       string         `json:"event_time"`
	EventProperties map[string]any `json:"event_properties"`
}

// cohortsResponse is the response from GET /api/3/cohorts.
type cohortsResponse struct {
	Cohorts []cohortEntry `json:"cohorts"`
}

// cohortEntry represents a behavioral cohort.
type cohortEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Size        *int   `json:"size,omitempty"`
	Archived    bool   `json:"archived"`
}

// --- Exported output types (AI-friendly) ---

// EventType represents an event type with its total active users.
type EventType struct {
	Name       string `json:"name"`
	TotalUsers int    `json:"total_users"`
}

// SegmentationResult holds event segmentation data.
type SegmentationResult struct {
	EventType string    `json:"event_type"`
	Dates     []string  `json:"dates"`
	Values    []float64 `json:"values"`
}

// TaxonomyEvent represents an event type schema entry.
type TaxonomyEvent struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// TaxonomyUserProperty represents a user property schema entry.
type TaxonomyUserProperty struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// FunnelResult holds funnel analysis data.
type FunnelResult struct {
	Steps []FunnelStep `json:"steps"`
}

// FunnelStep represents a single step in a funnel.
type FunnelStep struct {
	Name          string  `json:"name"`
	Count         int     `json:"count"`
	ConversionPct float64 `json:"conversion_pct"`
}

// RetentionResult holds retention analysis data.
type RetentionResult struct {
	StartEvent  string         `json:"start_event"`
	ReturnEvent string         `json:"return_event"`
	Days        []RetentionDay `json:"days"`
}

// RetentionDay represents a single day in a retention curve.
type RetentionDay struct {
	Day   int     `json:"day"`
	Count int     `json:"count"`
	Pct   float64 `json:"pct"`
}

// UserMatch represents a user found by search.
type UserMatch struct {
	AmplitudeID int64  `json:"amplitude_id"`
	UserID      string `json:"user_id"`
	Platform    string `json:"platform"`
	Country     string `json:"country"`
	LastSeen    string `json:"last_seen"`
}

// UserActivity holds a user's event history.
type UserActivity struct {
	AmplitudeID int64           `json:"amplitude_id"`
	Events      []ActivityEvent `json:"events"`
}

// ActivityEvent represents a single event in a user's history.
type ActivityEvent struct {
	Type       string         `json:"type"`
	Time       string         `json:"time"`
	Properties map[string]any `json:"properties,omitempty"`
}

// Cohort represents a behavioral cohort.
type Cohort struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Size        *int   `json:"size,omitempty"`
	Archived    bool   `json:"archived"`
}
