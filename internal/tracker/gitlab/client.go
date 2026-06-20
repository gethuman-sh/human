package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/tracker"
)

var _ tracker.Provider = (*Client)(nil)

// Client is a GitLab REST API (v4) client that implements tracker.Provider.
type Client struct {
	api *apiclient.Client
}

// New creates a GitLab client with the given base URL and private token.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.HeaderAuth("PRIVATE-TOKEN", token)),
			apiclient.WithURLBuilder(apiclient.RawPathURL()),
			apiclient.WithProviderName("gitlab"),
			apiclient.WithErrorFormatter(func(_, method, path string, statusCode int, body []byte) error {
				safePath := strings.ReplaceAll(path, "%", "%%")
				return errors.WithDetails(
					"gitlab "+method+" "+safePath+" returned "+strconv.Itoa(statusCode)+": "+string(body),
					"statusCode", statusCode, "method", method, "path", path)
			}),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// clampPerPage bounds a requested page size to GitLab's accepted 1..100 range;
// a zero or negative MaxResults falls back to a sane default.
func clampPerPage(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 100:
		return 100
	default:
		return n
	}
}

// ListIssues implements tracker.Lister.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	var path string
	if opts.Project != "" {
		encodedProject, err := splitProject(opts.Project)
		if err != nil {
			return nil, err
		}
		path = fmt.Sprintf("/api/v4/projects/%s/issues", encodedProject)
	} else {
		path = "/api/v4/issues"
	}

	query := url.Values{
		"per_page": {fmt.Sprintf("%d", clampPerPage(opts.MaxResults))},
	}
	if opts.Project == "" {
		query.Set("scope", "all")
	}
	if !opts.IncludeAll {
		query.Set("state", "opened")
	}
	if !opts.UpdatedSince.IsZero() {
		query.Set("updated_after", opts.UpdatedSince.Format(time.RFC3339))
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var glIssues []glIssue
	if err := apiclient.DecodeJSON(resp, &glIssues, "project", opts.Project); err != nil {
		return nil, err
	}

	issues := make([]tracker.Issue, 0, len(glIssues))
	for _, gi := range glIssues {
		project := opts.Project
		if project == "" {
			project = projectFromIssue(gi)
		}
		issues = append(issues, toTrackerIssue(project, gi))
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	project, iid, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var gi glIssue
	if err := apiclient.DecodeJSON(resp, &gi, "issueKey", key); err != nil {
		return nil, err
	}

	issue := toTrackerIssue(project, gi)
	return &issue, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	// GitLab's REST v4 API has no parent/child relationship for issues — that
	// lives only in the GraphQL work-items API. Fail loudly rather than
	// silently dropping the parent the caller asked for.
	if issue.ParentKey != "" {
		return nil, errors.WithDetails(
			"gitlab does not support subtasks via the REST API; issue parent/child links require GitLab's GraphQL work-items API",
			"parentKey", issue.ParentKey)
	}

	encodedProject, err := splitProject(issue.Project)
	if err != nil {
		return nil, err
	}

	payload := createRequest{
		Title:       issue.Title,
		Description: issue.Description,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request",
			"project", issue.Project)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues", encodedProject)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var result createResponse
	if err := apiclient.DecodeJSON(resp, &result, "project", issue.Project); err != nil {
		return nil, err
	}

	return &tracker.Issue{
		Key:         fmt.Sprintf("%s#%d", issue.Project, result.IID),
		Project:     issue.Project,
		Title:       result.Title,
		Description: result.Description,
		URL:         result.WebURL,
	}, nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	project, iid, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(noteRequest{Body: body})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling note request",
			"issueKey", issueKey)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d/notes", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var gn glNote
	if err := apiclient.DecodeJSON(resp, &gn, "issueKey", issueKey); err != nil {
		return nil, err
	}

	return toTrackerComment(gn)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	project, iid, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d/notes", encodedProject, iid)
	query := url.Values{
		"sort": {"asc"},
	}
	resp, err := c.doRequest(ctx, http.MethodGet, path, query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var glNotes []glNote
	if err := apiclient.DecodeJSON(resp, &glNotes, "issueKey", issueKey); err != nil {
		return nil, err
	}

	comments := make([]tracker.Comment, 0, len(glNotes))
	for _, gn := range glNotes {
		if gn.System {
			continue
		}
		comment, err := toTrackerComment(gn)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *comment)
	}
	return comments, nil
}

func toTrackerComment(gn glNote) (*tracker.Comment, error) {
	created, err := time.Parse(time.RFC3339, gn.CreatedAt)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing note timestamp",
			"noteID", gn.ID)
	}

	author := ""
	if gn.Author != nil {
		author = gn.Author.Username
	}

	return &tracker.Comment{
		ID:      strconv.Itoa(gn.ID),
		Author:  author,
		Body:    gn.Body,
		Created: created,
	}, nil
}

// ListStatuses implements tracker.StatusLister.
// GitLab issues have fixed states: opened and closed.
func (c *Client) ListStatuses(_ context.Context, _ string) ([]tracker.Status, error) {
	return []tracker.Status{
		{Name: "opened", Category: tracker.CategoryStarted},
		{Name: "closed", Category: tracker.CategoryClosed},
	}, nil
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	lower := strings.ToLower(targetStatus)

	var event string
	switch lower {
	case "opened", "open", "reopen", "reopened":
		event = "reopen"
	case "closed", "close":
		event = "close"
	default:
		return errors.WithDetails("GitLab only supports 'opened' and 'closed' states",
			"key", key, "targetStatus", targetStatus)
	}

	project, iid, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{"state_event": event})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "key", key)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	project, iid, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return err
	}

	uid, err := strconv.Atoi(userID)
	if err != nil {
		return errors.WithDetails("invalid user ID, expected numeric", "userID", userID)
	}

	payload, err := json.Marshal(map[string][]int{"assignee_ids": {uid}})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "key", key)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v4/user", "", nil)
	if err != nil {
		return "", err
	}
	var user glCurrentUser
	if err := apiclient.DecodeJSON(resp, &user); err != nil {
		return "", err
	}
	return strconv.Itoa(user.ID), nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	project, iid, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return nil, err
	}

	fields := make(map[string]string)
	if opts.Title != nil {
		fields["title"] = *opts.Title
	}
	if opts.Description != nil {
		fields["description"] = *opts.Description
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "key", key)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var gi glIssue
	if err := apiclient.DecodeJSON(resp, &gi, "key", key); err != nil {
		return nil, err
	}

	issue := toTrackerIssue(project, gi)
	return &issue, nil
}

// DeleteIssue implements tracker.Deleter.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	project, iid, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	encodedProject, err := splitProject(project)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", encodedProject, iid)
	resp, err := c.doRequest(ctx, http.MethodDelete, path, "", nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string, body io.Reader) (*http.Response, error) {
	return c.api.Do(ctx, method, path, rawQuery, body)
}

// splitProject URL-encodes a project path for use in GitLab API URLs.
// For example, "mygroup/myproject" becomes "mygroup%2Fmyproject".
func splitProject(project string) (string, error) {
	slashIdx := strings.Index(project, "/")
	if slashIdx < 1 || slashIdx == len(project)-1 {
		return "", errors.WithDetails("invalid project format, expected namespace/project",
			"project", project)
	}
	return url.PathEscape(project), nil
}

// parseIssueKey parses a "namespace/project#IID" key into project path and IID.
func parseIssueKey(key string) (string, int, error) {
	hashIdx := strings.LastIndex(key, "#")
	if hashIdx < 0 {
		return "", 0, errors.WithDetails("invalid issue key format, expected namespace/project#IID",
			"key", key)
	}

	project := key[:hashIdx]
	iidStr := key[hashIdx+1:]

	if _, err := splitProject(project); err != nil {
		return "", 0, errors.WithDetails("invalid issue key format, expected namespace/project#IID",
			"key", key)
	}

	iid, err := strconv.Atoi(iidStr)
	if err != nil {
		return "", 0, errors.WithDetails("invalid issue IID in key",
			"key", key)
	}

	return project, iid, nil
}

// toTrackerIssue converts a GitLab API issue to a tracker.Issue.
// glStateCategory maps GitLab's opened/closed issue state to a tracker.Category,
// mirroring ListStatuses so issue listings carry the same semantics the TUI's
// pipeline-stage rendering depends on.
func glStateCategory(state string) tracker.Category {
	if state == "closed" {
		return tracker.CategoryClosed
	}
	return tracker.CategoryStarted
}

func toTrackerIssue(project string, gi glIssue) tracker.Issue {
	issue := tracker.Issue{
		Key:         fmt.Sprintf("%s#%d", project, gi.IID),
		Project:     project,
		Title:       gi.Title,
		Status:      gi.State,
		StatusType:  glStateCategory(gi.State),
		Description: gi.Description,
		URL:         gi.WebURL,
	}

	if gi.UpdatedAt != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, gi.UpdatedAt)
	}
	if len(gi.Labels) > 0 {
		issue.Type = gi.Labels[0]
		issue.Labels = append([]string(nil), gi.Labels...)
	}
	if len(gi.Assignees) > 0 {
		issue.Assignee = gi.Assignees[0].Username
	}
	if gi.Author != nil {
		issue.Reporter = gi.Author.Username
	}

	return issue
}

// projectFromIssue extracts the project path from a GitLab issue's references or web_url.
// The references.full field looks like "group/project#123".
func projectFromIssue(gi glIssue) string {
	if gi.References != nil && gi.References.Full != "" {
		ref := gi.References.Full
		if idx := strings.LastIndex(ref, "#"); idx > 0 {
			return ref[:idx]
		}
	}
	return ""
}
