package azuredevops

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

// Client is an Azure DevOps REST API client that implements tracker.Provider.
type Client struct {
	api     *apiclient.Client
	org     string
	baseURL string
}

// New creates an Azure DevOps client with the given base URL, organization, and PAT.
func New(baseURL, org, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BasicAuth("", token)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithProviderName("azuredevops"),
		),
		org:     org,
		baseURL: baseURL,
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListIssues implements tracker.Lister using WIQL to query work items.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	project := opts.Project

	var clauses []string
	if project != "" {
		clauses = append(clauses, fmt.Sprintf("[System.TeamProject] = '%s'", strings.ReplaceAll(project, "'", "''")))
	}
	if !opts.IncludeAll {
		clauses = append(clauses, "[System.State] <> 'Done' AND [System.State] <> 'Removed'")
	}
	if !opts.UpdatedSince.IsZero() {
		clauses = append(clauses, fmt.Sprintf("[System.ChangedDate] > '%s'", opts.UpdatedSince.Format("2006-01-02T15:04:05Z")))
	}

	query := "SELECT [System.Id] FROM workitems"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}

	wiqlBody, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling WIQL query", "project", project)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, c.apisPath(project, "wit/wiql"), "api-version=7.1", bytes.NewReader(wiqlBody), "application/json")
	if err != nil {
		return nil, err
	}
	var wiqlResp adoWIQLResponse
	if err := apiclient.DecodeJSON(resp, &wiqlResp, "project", project); err != nil {
		return nil, err
	}

	if len(wiqlResp.WorkItems) == 0 {
		return nil, nil
	}

	ids := make([]string, len(wiqlResp.WorkItems))
	for i, ref := range wiqlResp.WorkItems {
		ids[i] = strconv.Itoa(ref.ID)
	}

	batchQuery := url.Values{
		"ids":         {strings.Join(ids, ",")},
		"api-version": {"7.1"},
	}
	batchResp, err := c.doRequest(ctx, http.MethodGet, c.apisPath(project, "wit/workitems"), batchQuery.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	var batchResult struct {
		Value []adoWorkItem `json:"value"`
	}
	if err := apiclient.DecodeJSON(batchResp, &batchResult, "project", project); err != nil {
		return nil, err
	}

	issues := make([]tracker.Issue, 0, len(batchResult.Value))
	for _, wi := range batchResult.Value {
		p := project
		if p == "" {
			p = wi.Fields.TeamProject
		}
		issues = append(issues, c.toTrackerIssue(wi, p))
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	// $expand=relations surfaces the parent hierarchy link so subtasks can
	// report their parent. The batch list endpoint omits relations, so this
	// only applies to single-issue reads.
	resp, err := c.doRequest(ctx, http.MethodGet, path, "$expand=relations&api-version=7.1", nil, "")
	if err != nil {
		return nil, err
	}
	var wi adoWorkItem
	if err := apiclient.DecodeJSON(resp, &wi, "key", key); err != nil {
		return nil, err
	}

	issue := c.toTrackerIssue(wi, project)
	return &issue, nil
}

// CreateIssue implements tracker.Creator using JSON Patch format.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	project := issue.Project
	if project == "" {
		return nil, errors.WithDetails("project is required for Azure DevOps")
	}

	ops := []patchOp{
		{Op: "add", Path: "/fields/System.Title", Value: issue.Title},
	}
	if issue.Description != "" {
		ops = append(ops, patchOp{Op: "add", Path: "/fields/System.Description", Value: issue.Description})
	}
	// Azure models a subtask as a hierarchy link to the parent work item
	// (Hierarchy-Reverse points from child to parent), added at create time.
	if issue.ParentKey != "" {
		_, parentID, err := parseIssueKey(issue.ParentKey)
		if err != nil {
			return nil, errors.WrapWithDetails(err, "invalid parent key", "parentKey", issue.ParentKey)
		}
		ops = append(ops, patchOp{
			Op:   "add",
			Path: "/relations/-",
			Value: adoRelation{
				Rel: hierarchyReverseRel,
				URL: c.workItemURL(parentID),
			},
		})
	}

	body, err := json.Marshal(ops)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request", "project", project)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/$Issue", url.PathEscape(c.org), url.PathEscape(project))
	resp, err := c.doRequest(ctx, http.MethodPost, path, "api-version=7.1", bytes.NewReader(body), "application/json-patch+json")
	if err != nil {
		return nil, err
	}
	var wi adoWorkItem
	if err := apiclient.DecodeJSON(resp, &wi, "project", project); err != nil {
		return nil, err
	}

	return &tracker.Issue{
		Key:         fmt.Sprintf("%s/%d", project, wi.ID),
		Project:     project,
		Title:       wi.Fields.Title,
		Description: wi.Fields.Description,
		URL:         fmt.Sprintf("https://dev.azure.com/%s/%s/_workitems/edit/%d", c.org, project, wi.ID),
		ParentKey:   issue.ParentKey,
	}, nil
}

// adoCategoryToType maps an Azure DevOps state category to a tracker.Category.
func adoCategoryToType(category string) tracker.Category {
	switch category {
	case "Proposed":
		return tracker.CategoryUnstarted
	case "InProgress":
		return tracker.CategoryStarted
	case "Resolved", "Completed":
		return tracker.CategoryDone
	case "Removed":
		return tracker.CategoryClosed
	default:
		return tracker.CategoryUnknown
	}
}

// ListStatuses implements tracker.StatusLister.
func (c *Client) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	// Fetch the work item to determine its type.
	wiPath := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	wiResp, err := c.doRequest(ctx, http.MethodGet, wiPath, "api-version=7.1", nil, "")
	if err != nil {
		return nil, err
	}
	var wi adoWorkItem
	if err := apiclient.DecodeJSON(wiResp, &wi, "key", key); err != nil {
		return nil, err
	}

	wiType := wi.Fields.WorkItemType
	if wiType == "" {
		return nil, errors.WithDetails("work item has no type", "key", key)
	}

	// Fetch states for that work item type. Every segment is escaped
	// so org / project names containing characters like spaces or
	// slashes can't inject additional URL path elements.
	statesPath := fmt.Sprintf("/%s/%s/_apis/wit/workitemtypes/%s/states",
		url.PathEscape(c.org), url.PathEscape(project), url.PathEscape(wiType))
	statesResp, err := c.doRequest(ctx, http.MethodGet, statesPath, "api-version=7.1", nil, "")
	if err != nil {
		return nil, err
	}
	var statesResult adoWorkItemTypeStatesResponse
	if err := apiclient.DecodeJSON(statesResp, &statesResult, "key", key, "type", wiType); err != nil {
		return nil, err
	}

	statuses := make([]tracker.Status, len(statesResult.Value))
	for i, s := range statesResult.Value {
		statuses[i] = tracker.Status{
			Name:     s.Name,
			Category: adoCategoryToType(s.Category),
		}
	}
	return statuses, nil
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	ops := []patchOp{
		{Op: "add", Path: "/fields/System.State", Value: targetStatus},
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "key", key)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "api-version=7.1", bytes.NewReader(body), "application/json-patch+json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	ops := []patchOp{
		{Op: "add", Path: "/fields/System.AssignedTo", Value: userID},
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "key", key)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "api-version=7.1", bytes.NewReader(body), "application/json-patch+json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	path := fmt.Sprintf("/%s/_apis/connectionData", url.PathEscape(c.org))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "api-version=7.1", nil, "")
	if err != nil {
		return "", err
	}
	var result adoConnectionData
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return "", err
	}
	return result.AuthenticatedUser.UniqueName, nil
}

// EditIssue implements tracker.Editor using JSON Patch format.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return nil, err
	}

	var ops []patchOp
	if opts.Title != nil {
		ops = append(ops, patchOp{Op: "replace", Path: "/fields/System.Title", Value: *opts.Title})
	}
	if opts.Description != nil {
		ops = append(ops, patchOp{Op: "replace", Path: "/fields/System.Description", Value: *opts.Description})
	}

	body, err := json.Marshal(ops)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "key", key)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "api-version=7.1", bytes.NewReader(body), "application/json-patch+json")
	if err != nil {
		return nil, err
	}
	var wi adoWorkItem
	if err := apiclient.DecodeJSON(resp, &wi, "key", key); err != nil {
		return nil, err
	}

	issue := c.toTrackerIssue(wi, project)
	return &issue, nil
}

// DeleteIssue implements tracker.Deleter by transitioning the work item to "Done".
// Azure DevOps does not support true deletion via the REST API.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	project, id, err := parseIssueKey(key)
	if err != nil {
		return err
	}

	ops := []patchOp{
		{Op: "add", Path: "/fields/System.State", Value: "Done"},
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling delete request", "key", key)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodPatch, path, "api-version=7.1", bytes.NewReader(body), "application/json-patch+json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	project, id, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]string{"text": body})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling comment request", "issueKey", issueKey)
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workItems/%d/comments", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "api-version=7.1-preview.4", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var ac adoComment
	if err := apiclient.DecodeJSON(resp, &ac, "issueKey", issueKey); err != nil {
		return nil, err
	}

	return toTrackerComment(ac)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	project, id, err := parseIssueKey(issueKey)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/%s/%s/_apis/wit/workItems/%d/comments", url.PathEscape(c.org), url.PathEscape(project), id)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "api-version=7.1-preview.4", nil, "")
	if err != nil {
		return nil, err
	}
	var cl adoCommentList
	if err := apiclient.DecodeJSON(resp, &cl, "issueKey", issueKey); err != nil {
		return nil, err
	}

	comments := make([]tracker.Comment, 0, len(cl.Comments))
	for _, ac := range cl.Comments {
		tc, err := toTrackerComment(ac)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *tc)
	}
	return comments, nil
}

func toTrackerComment(ac adoComment) (*tracker.Comment, error) {
	created, err := time.Parse(time.RFC3339, ac.CreatedDate)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing comment timestamp", "commentID", ac.ID)
	}

	author := ""
	if ac.CreatedBy != nil {
		author = ac.CreatedBy.DisplayName
	}

	return &tracker.Comment{
		ID:      strconv.Itoa(ac.ID),
		Author:  author,
		Body:    ac.Text,
		Created: created,
	}, nil
}

func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string, body io.Reader, contentType string) (*http.Response, error) {
	if contentType != "" {
		return c.api.DoWithContentType(ctx, method, path, rawQuery, body, contentType)
	}
	return c.api.Do(ctx, method, path, rawQuery, body)
}

// parseIssueKey parses a "Project/ID" key into project name and numeric ID.
func parseIssueKey(key string) (string, int, error) {
	slashIdx := strings.LastIndex(key, "/")
	if slashIdx < 0 {
		return "", 0, errors.WithDetails("invalid issue key format, expected Project/ID",
			"key", key)
	}

	project := key[:slashIdx]
	if project == "" {
		return "", 0, errors.WithDetails("invalid issue key format, expected Project/ID",
			"key", key)
	}

	idStr := key[slashIdx+1:]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return "", 0, errors.WithDetails("invalid work item ID in key",
			"key", key)
	}

	return project, id, nil
}

// toTrackerIssue converts an Azure DevOps work item to a tracker.Issue.
func (c *Client) toTrackerIssue(wi adoWorkItem, project string) tracker.Issue {
	issue := tracker.Issue{
		Key:         fmt.Sprintf("%s/%d", project, wi.ID),
		Project:     project,
		Type:        wi.Fields.WorkItemType,
		Title:       wi.Fields.Title,
		Status:      wi.Fields.State,
		Description: wi.Fields.Description,
		URL:         fmt.Sprintf("https://dev.azure.com/%s/%s/_workitems/edit/%d", c.org, project, wi.ID),
	}

	if wi.Fields.ChangedDate != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, wi.Fields.ChangedDate)
	}
	if wi.Fields.Priority > 0 {
		issue.Priority = strconv.Itoa(wi.Fields.Priority)
	}
	if wi.Fields.AssignedTo != nil {
		issue.Assignee = wi.Fields.AssignedTo.DisplayName
	}
	if wi.Fields.CreatedBy != nil {
		issue.Reporter = wi.Fields.CreatedBy.DisplayName
	}
	// Work item URLs are org-scoped (no project segment), so the parent is
	// assumed to live in the same project as the child for key reconstruction.
	if parentID := parentIDFromRelations(wi.Relations); parentID != "" {
		issue.ParentKey = project + "/" + parentID
	}

	return issue
}

// hierarchyReverseRel is the Azure DevOps link type pointing from a child work
// item to its parent.
const hierarchyReverseRel = "System.LinkTypes.Hierarchy-Reverse"

// workItemURL builds the org-scoped REST URL Azure expects when linking to a
// work item by ID.
func (c *Client) workItemURL(id int) string {
	return fmt.Sprintf("%s/%s/_apis/wit/workItems/%d", strings.TrimRight(c.baseURL, "/"), url.PathEscape(c.org), id)
}

// parentIDFromRelations returns the numeric ID of the parent work item from a
// hierarchy-reverse relation, or "" when the item has no parent.
func parentIDFromRelations(relations []adoRelation) string {
	for _, rel := range relations {
		if rel.Rel == hierarchyReverseRel {
			if idx := strings.LastIndex(rel.URL, "/"); idx >= 0 && idx < len(rel.URL)-1 {
				return rel.URL[idx+1:]
			}
		}
	}
	return ""
}

// apisPath builds an Azure DevOps API path, scoped to a project when provided
// or to the organization when project is empty.
func (c *Client) apisPath(project, resource string) string {
	if project != "" {
		return fmt.Sprintf("/%s/%s/_apis/%s", url.PathEscape(c.org), url.PathEscape(project), resource)
	}
	return fmt.Sprintf("/%s/_apis/%s", url.PathEscape(c.org), resource)
}
