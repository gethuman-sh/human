package github

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

// Client is a GitHub REST API client that implements tracker.Lister,
// tracker.Getter, and tracker.Creator.
type Client struct {
	api *apiclient.Client
}

// New creates a GitHub client with the given base URL and personal access token.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BearerToken(token)),
			apiclient.WithHeader("Accept", "application/vnd.github+json"),
			apiclient.WithProviderName("github"),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListIssues implements tracker.Lister.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	if opts.Project == "" {
		return c.listAllIssues(ctx, opts)
	}

	owner, repo, err := splitProject(opts.Project)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
	state := "open"
	if opts.IncludeAll {
		state = "all"
	}
	query := url.Values{
		"per_page": {fmt.Sprintf("%d", clampPerPage(opts.MaxResults))},
		"state":    {state},
	}
	if !opts.UpdatedSince.IsZero() {
		query.Set("since", opts.UpdatedSince.Format(time.RFC3339))
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var ghIssues []ghIssue
	if err := apiclient.DecodeJSON(resp, &ghIssues, "project", opts.Project); err != nil {
		return nil, err
	}

	var issues []tracker.Issue
	for _, gi := range ghIssues {
		if gi.PullRequest != nil {
			continue
		}
		issues = append(issues, toTrackerIssue(owner, repo, gi))
	}
	return issues, nil
}

// clampPerPage bounds a requested page size to GitHub's accepted 1..100 range;
// a zero or negative MaxResults falls back to a sane default rather than
// producing an undefined per_page value.
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

// listAllIssues uses GET /search/issues to list issues across all repos.
func (c *Client) listAllIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	q := "is:issue"
	if !opts.IncludeAll {
		q += " is:open"
	}

	query := url.Values{
		"q":        {q},
		"per_page": {fmt.Sprintf("%d", clampPerPage(opts.MaxResults))},
		"sort":     {"created"},
		"order":    {"desc"},
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/search/issues", query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var result ghSearchResult
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return nil, err
	}

	var issues []tracker.Issue
	for _, item := range result.Items {
		if item.PullRequest != nil {
			continue
		}
		owner, repo := parseRepoURL(item.RepositoryURL)
		issues = append(issues, toTrackerIssue(owner, repo, item.ghIssue))
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	owner, repo, number, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var gi ghIssue
	if err := apiclient.DecodeJSON(resp, &gi, "issueKey", key); err != nil {
		return nil, err
	}

	issue := toTrackerIssue(owner, repo, gi)
	return &issue, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	owner, repo, err := splitProject(issue.Project)
	if err != nil {
		return nil, err
	}

	payload := createRequest{
		Title: issue.Title,
		Body:  issue.Description,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request",
			"project", issue.Project)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var result createResponse
	if err := apiclient.DecodeJSON(resp, &result, "project", issue.Project); err != nil {
		return nil, err
	}

	// GitHub has no parent field on create — the sub-issue link is a separate
	// call keyed by the new issue's internal ID, made only once it exists.
	if issue.ParentKey != "" {
		if err := c.addSubIssue(ctx, issue.ParentKey, result.ID); err != nil {
			return nil, err
		}
	}

	return &tracker.Issue{
		Key:         fmt.Sprintf("%s/%s#%d", owner, repo, result.Number),
		Project:     issue.Project,
		Title:       result.Title,
		Description: result.Body,
		URL:         fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, result.Number),
		ParentKey:   issue.ParentKey,
	}, nil
}

// addSubIssue links a freshly created issue under a parent via GitHub's
// sub-issues API. The parent is addressed by its issue number, while the child
// is referenced by its internal ID (not its number).
func (c *Client) addSubIssue(ctx context.Context, parentKey string, childID int) error {
	owner, repo, number, err := parseIssueKey(parentKey)
	if err != nil {
		return errors.WrapWithDetails(err, "invalid parent key", "parentKey", parentKey)
	}

	payload, err := json.Marshal(map[string]int{"sub_issue_id": childID})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling sub-issue request", "parentKey", parentKey)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d/sub_issues", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	owner, repo, number, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(commentRequest{Body: body})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling comment request",
			"issueKey", issueKey)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var gc ghComment
	if err := apiclient.DecodeJSON(resp, &gc, "issueKey", issueKey); err != nil {
		return nil, err
	}

	return toTrackerComment(gc)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	owner, repo, number, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var ghComments []ghComment
	if err := apiclient.DecodeJSON(resp, &ghComments, "issueKey", issueKey); err != nil {
		return nil, err
	}

	comments := make([]tracker.Comment, 0, len(ghComments))
	for _, gc := range ghComments {
		c, err := toTrackerComment(gc)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *c)
	}
	return comments, nil
}

func toTrackerComment(gc ghComment) (*tracker.Comment, error) {
	created, err := time.Parse(time.RFC3339, gc.CreatedAt)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing comment timestamp",
			"commentID", gc.ID)
	}

	author := ""
	if gc.User != nil {
		author = gc.User.Login
	}

	return &tracker.Comment{
		ID:      strconv.Itoa(gc.ID),
		Author:  author,
		Body:    gc.Body,
		Created: created,
	}, nil
}

// ListStatuses implements tracker.StatusLister.
// GitHub issues have fixed states: open and closed.
func (c *Client) ListStatuses(_ context.Context, _ string) ([]tracker.Status, error) {
	return []tracker.Status{
		{Name: "open", Category: tracker.CategoryStarted},
		{Name: "closed", Category: tracker.CategoryClosed},
	}, nil
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	lower := strings.ToLower(targetStatus)
	if lower != "open" && lower != "closed" {
		return errors.WithDetails("GitHub only supports 'open' and 'closed' states",
			"key", key, "targetStatus", targetStatus)
	}

	owner, repo, number, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{"state": lower})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "key", key)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	owner, repo, number, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string][]string{"assignees": {userID}})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "key", key)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/user", "", nil)
	if err != nil {
		return "", err
	}
	var user ghCurrentUser
	if err := apiclient.DecodeJSON(resp, &user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	owner, repo, number, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	fields := make(map[string]string)
	if opts.Title != nil {
		fields["title"] = *opts.Title
	}
	if opts.Description != nil {
		fields["body"] = *opts.Description
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "key", key)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	var gi ghIssue
	if err := apiclient.DecodeJSON(resp, &gi, "key", key); err != nil {
		return nil, err
	}

	issue := toTrackerIssue(owner, repo, gi)
	return &issue, nil
}

// DeleteIssue implements tracker.Deleter by closing the issue.
// GitHub does not support true deletion via the API, so we close the issue instead.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	owner, repo, number, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{"state": "closed"})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling delete request", "key", key)
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	// Decode the response and verify the state actually transitioned
	// to closed. A workflow rule on the repository can silently
	// re-open the issue under the same HTTP 200, so a bare status
	// check would have the audit log record success for a no-op.
	var ghIssue struct {
		State string `json:"state"`
	}
	if decodeErr := apiclient.DecodeJSON(resp, &ghIssue); decodeErr != nil {
		return errors.WrapWithDetails(decodeErr, "decoding close response", "key", key)
	}
	if ghIssue.State != "closed" {
		return errors.WithDetails("issue did not transition to closed",
			"key", key, "state", ghIssue.State)
	}
	return nil
}

func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string, body io.Reader) (*http.Response, error) {
	return c.api.Do(ctx, method, path, rawQuery, body)
}

// splitProject parses an "owner/repo" string.
func splitProject(project string) (string, string, error) {
	parts := strings.SplitN(project, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.WithDetails("invalid project format, expected owner/repo",
			"project", project)
	}
	return parts[0], parts[1], nil
}

// parseIssueKey parses an "owner/repo#123" key into its components.
func parseIssueKey(key string) (string, string, int, error) {
	hashIdx := strings.LastIndex(key, "#")
	if hashIdx < 0 {
		return "", "", 0, errors.WithDetails("invalid issue key format, expected owner/repo#number",
			"key", key)
	}

	project := key[:hashIdx]
	numberStr := key[hashIdx+1:]

	owner, repo, err := splitProject(project)
	if err != nil {
		return "", "", 0, errors.WithDetails("invalid issue key format, expected owner/repo#number",
			"key", key)
	}

	number, err := strconv.Atoi(numberStr)
	if err != nil {
		return "", "", 0, errors.WithDetails("invalid issue number in key",
			"key", key)
	}

	return owner, repo, number, nil
}

// toTrackerIssue converts a GitHub API issue to a tracker.Issue.
// ghStateCategory maps GitHub's open/closed issue state to a tracker.Category,
// mirroring ListStatuses so issue listings carry the same semantics the TUI's
// pipeline-stage rendering depends on.
func ghStateCategory(state string) tracker.Category {
	if state == "closed" {
		return tracker.CategoryClosed
	}
	return tracker.CategoryStarted
}

func toTrackerIssue(owner, repo string, gi ghIssue) tracker.Issue {
	issue := tracker.Issue{
		Key:         fmt.Sprintf("%s/%s#%d", owner, repo, gi.Number),
		Project:     fmt.Sprintf("%s/%s", owner, repo),
		Title:       gi.Title,
		Status:      gi.State,
		StatusType:  ghStateCategory(gi.State),
		Description: gi.Body,
		URL:         gi.HTMLURL,
	}

	if gi.UpdatedAt != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, gi.UpdatedAt)
	}
	if len(gi.Labels) > 0 {
		issue.Type = gi.Labels[0].Name
		issue.Labels = make([]string, 0, len(gi.Labels))
		for _, l := range gi.Labels {
			issue.Labels = append(issue.Labels, l.Name)
		}
	}
	if gi.Assignee != nil {
		issue.Assignee = gi.Assignee.Login
	}
	if gi.User != nil {
		issue.Reporter = gi.User.Login
	}

	return issue
}

// parseRepoURL extracts owner and repo from a GitHub API repository URL.
// e.g. "https://api.github.com/repos/owner/repo" → ("owner", "repo").
func parseRepoURL(repoURL string) (string, string) {
	// Find "/repos/" and split what follows.
	const prefix = "/repos/"
	idx := strings.Index(repoURL, prefix)
	if idx < 0 {
		return "", ""
	}
	parts := strings.SplitN(repoURL[idx+len(prefix):], "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
