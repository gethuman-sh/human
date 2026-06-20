package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/jira/adf"
)

var _ tracker.Provider = (*Client)(nil)

// Client is a Jira REST API client that implements tracker.Lister and tracker.Getter.
type Client struct {
	api     *apiclient.Client
	baseURL string
}

// New creates a Jira client with the given base URL, user email, and API key.
func New(baseURL, user, key string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BasicAuth(user, key)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithProviderName("jira"),
		),
		baseURL: baseURL,
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListIssues implements tracker.Lister.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	var clauses []string
	if opts.Project != "" {
		clauses = append(clauses, fmt.Sprintf("project=\"%s\"", strings.ReplaceAll(opts.Project, "\"", "\\\"")))
	}
	if !opts.IncludeAll {
		clauses = append(clauses, "statusCategory != Done")
	}
	if !opts.UpdatedSince.IsZero() {
		clauses = append(clauses, fmt.Sprintf(`updated >= "%s"`, opts.UpdatedSince.Format("2006-01-02 15:04")))
	}
	jql := strings.Join(clauses, " AND ")
	if jql == "" {
		jql = "order by created DESC"
	} else {
		jql += " order by created DESC"
	}
	query := url.Values{
		"jql":        {jql},
		"maxResults": {fmt.Sprintf("%d", opts.MaxResults)},
		"fields":     {"*navigable"},
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/rest/api/3/search/jql", query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var result searchResult
	if err := apiclient.DecodeJSON(resp, &result, "project", opts.Project); err != nil {
		return nil, err
	}

	issues := make([]tracker.Issue, len(result.Issues))
	for i, iss := range result.Issues {
		issues[i] = tracker.Issue{
			Key:     iss.Key,
			Project: projectFromKey(iss.Key),
			Title:   iss.Fields.Summary,
			Type:    iss.Fields.IssueType.Name,
			Status:  iss.Fields.Status.Name,
			URL:     c.baseURL + "/browse/" + iss.Key,
		}
		if iss.Fields.Updated != "" {
			issues[i].UpdatedAt, _ = time.Parse("2006-01-02T15:04:05.000-0700", iss.Fields.Updated)
		}
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s", url.PathEscape(key))
	query := url.Values{
		"fields": {"summary,status,description,assignee,reporter,priority,issuetype,parent"},
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var detail issueDetail
	if err := apiclient.DecodeJSON(resp, &detail, "issueKey", key); err != nil {
		return nil, err
	}

	f := detail.Fields
	desc := ""
	if hasDescription(f.Description) {
		var doc map[string]any
		if err := json.Unmarshal(f.Description, &doc); err != nil {
			return nil, errors.WrapWithDetails(err, "parsing description ADF",
				"issueKey", key)
		}
		desc = adf.ToMarkdown(doc)
	}

	parentKey := ""
	if f.Parent != nil {
		parentKey = f.Parent.Key
	}

	return &tracker.Issue{
		Key:         detail.Key,
		Title:       f.Summary,
		Type:        f.IssueType.Name,
		Status:      f.Status.Name,
		Priority:    nameOrEmpty(f.Priority),
		Assignee:    nameOrEmpty(f.Assignee),
		Reporter:    nameOrEmpty(f.Reporter),
		Description: desc,
		URL:         c.baseURL + "/browse/" + detail.Key,
		ParentKey:   parentKey,
	}, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	payload := createRequest{
		Fields: createFields{
			Project:     keyField{Key: issue.Project},
			Summary:     issue.Title,
			IssueType:   nameOnly{Name: issue.Type},
			Description: adf.FromMarkdown(issue.Description),
		},
	}
	// Jira models subtasks as a parent link; the issue type must be a
	// subtask-type (e.g. "Sub-task") for the server to accept it.
	if issue.ParentKey != "" {
		payload.Fields.Parent = &keyField{Key: issue.ParentKey}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request",
			"project", issue.Project)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/rest/api/3/issue", "", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var result createResponse
	if err := apiclient.DecodeJSON(resp, &result, "project", issue.Project); err != nil {
		return nil, err
	}

	return &tracker.Issue{
		Key:         result.Key,
		Project:     issue.Project,
		Type:        issue.Type,
		Title:       issue.Title,
		Description: issue.Description,
		URL:         c.baseURL + "/browse/" + result.Key,
		ParentKey:   issue.ParentKey,
	}, nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	payload := commentBody{Body: adf.FromMarkdown(body)}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling comment request",
			"issueKey", issueKey)
	}

	path := fmt.Sprintf("/rest/api/3/issue/%s/comment", url.PathEscape(issueKey))
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var jc jiraComment
	if err := apiclient.DecodeJSON(resp, &jc, "issueKey", issueKey); err != nil {
		return nil, err
	}

	return toTrackerComment(jc)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s/comment", url.PathEscape(issueKey))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var result commentsResponse
	if err := apiclient.DecodeJSON(resp, &result, "issueKey", issueKey); err != nil {
		return nil, err
	}

	comments := make([]tracker.Comment, 0, len(result.Comments))
	for _, jc := range result.Comments {
		c, err := toTrackerComment(jc)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *c)
	}
	return comments, nil
}

func toTrackerComment(jc jiraComment) (*tracker.Comment, error) {
	created, err := time.Parse("2006-01-02T15:04:05.000-0700", jc.Created)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing comment timestamp",
			"commentID", jc.ID)
	}

	body := ""
	if len(jc.Body) > 0 && string(jc.Body) != "null" {
		var doc map[string]any
		if err := json.Unmarshal(jc.Body, &doc); err != nil {
			return nil, errors.WrapWithDetails(err, "parsing comment ADF body",
				"commentID", jc.ID)
		}
		body = adf.ToMarkdown(doc)
	}

	return &tracker.Comment{
		ID:      jc.ID,
		Author:  nameOrEmpty(jc.Author),
		Body:    body,
		Created: created,
	}, nil
}

// fetchTransitions fetches available transitions for an issue.
func (c *Client) fetchTransitions(ctx context.Context, key string) ([]jiraTransition, error) {
	path := fmt.Sprintf("/rest/api/3/issue/%s/transitions", url.PathEscape(key))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var result transitionsResponse
	if err := apiclient.DecodeJSON(resp, &result, "issueKey", key); err != nil {
		return nil, err
	}
	return result.Transitions, nil
}

// ListStatuses implements tracker.StatusLister.
// Returns the available transitions from the issue's current state.
func (c *Client) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	transitions, err := c.fetchTransitions(ctx, key)
	if err != nil {
		return nil, err
	}

	statuses := make([]tracker.Status, len(transitions))
	for i, t := range transitions {
		statuses[i] = tracker.Status{Name: t.To.Name}
	}
	return statuses, nil
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	transitions, err := c.fetchTransitions(ctx, key)
	if err != nil {
		return err
	}

	var transitionID string
	for _, t := range transitions {
		if strings.EqualFold(t.To.Name, targetStatus) {
			transitionID = t.ID
			break
		}
	}
	if transitionID == "" {
		names := make([]string, len(transitions))
		for i, t := range transitions {
			names[i] = t.To.Name
		}
		return errors.WithDetails("transition not found",
			"issueKey", key, "targetStatus", targetStatus, "available", strings.Join(names, ", "))
	}

	path := fmt.Sprintf("/rest/api/3/issue/%s/transitions", url.PathEscape(key))
	payload, err := json.Marshal(map[string]any{"transition": map[string]string{"id": transitionID}})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "issueKey", key)
	}

	resp2, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp2.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	path := fmt.Sprintf("/rest/api/3/issue/%s/assignee", url.PathEscape(key))
	payload, err := json.Marshal(map[string]string{"accountId": userID})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "issueKey", key)
	}

	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/rest/api/3/myself", "", nil)
	if err != nil {
		return "", err
	}
	var result myselfResponse
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return "", err
	}
	return result.AccountID, nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	fields := make(map[string]any)
	if opts.Title != nil {
		fields["summary"] = *opts.Title
	}
	if opts.Description != nil {
		fields["description"] = adf.FromMarkdown(*opts.Description)
	}

	payload, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "issueKey", key)
	}

	path := fmt.Sprintf("/rest/api/3/issue/%s", url.PathEscape(key))
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()

	return c.GetIssue(ctx, key)
}

// DeleteIssue implements tracker.Deleter.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	path := fmt.Sprintf("/rest/api/3/issue/%s", url.PathEscape(key))
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

func hasDescription(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func nameOrEmpty(f *nameField) string {
	if f == nil {
		return ""
	}
	if f.DisplayName != "" {
		return f.DisplayName
	}
	return f.Name
}

// projectFromKey extracts the project key from an issue key like "KAN-123".
func projectFromKey(key string) string {
	if idx := strings.LastIndex(key, "-"); idx > 0 {
		return key[:idx]
	}
	return ""
}
