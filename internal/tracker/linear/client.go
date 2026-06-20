package linear

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/tracker"
)

var _ tracker.Provider = (*Client)(nil)

const listIssuesQuery = `query($teamKey: String!, $first: Int!) {
	issues(filter: { team: { key: { eq: $teamKey } } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listOpenIssuesQuery = `query($teamKey: String!, $first: Int!) {
	issues(filter: { team: { key: { eq: $teamKey } }, state: { type: { nin: ["completed", "canceled"] } } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listIssuesUpdatedSinceQuery = `query($teamKey: String!, $first: Int!, $since: DateTimeOrDuration!) {
	issues(filter: { team: { key: { eq: $teamKey } }, updatedAt: { gte: $since } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listOpenIssuesUpdatedSinceQuery = `query($teamKey: String!, $first: Int!, $since: DateTimeOrDuration!) {
	issues(filter: { team: { key: { eq: $teamKey } }, state: { type: { nin: ["completed", "canceled"] } }, updatedAt: { gte: $since } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

// All-teams query variants (no team filter — used when --project is omitted).
const listAllIssuesQuery = `query($first: Int!) {
	issues(first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listAllOpenIssuesQuery = `query($first: Int!) {
	issues(filter: { state: { type: { nin: ["completed", "canceled"] } } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listAllIssuesUpdatedSinceQuery = `query($first: Int!, $since: DateTimeOrDuration!) {
	issues(filter: { updatedAt: { gte: $since } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const listAllOpenIssuesUpdatedSinceQuery = `query($first: Int!, $since: DateTimeOrDuration!) {
	issues(filter: { state: { type: { nin: ["completed", "canceled"] } }, updatedAt: { gte: $since } }, first: $first, orderBy: createdAt) {
		nodes { identifier url title description updatedAt state { name type } priorityLabel
			assignee { name } creator { name } labels { nodes { name } } }
	}
}`

const getIssueQuery = `query($id: String!) {
	issue(id: $id) {
		identifier url title description state { name type } priorityLabel
		assignee { name } creator { name } labels { nodes { name } }
		parent { identifier }
	}
}`

const getTeamIDQuery = `query($key: String!) {
	teams(filter: { key: { eq: $key } }) { nodes { id } }
}`

const getProjectIDQuery = `query($name: String!) {
	projects(filter: { name: { eq: $name } }) { nodes { id name } }
}`

const getIssueIDQuery = `query($id: String!) {
	issue(id: $id) { id }
}`

const listCommentsQuery = `query($id: String!) {
	issue(id: $id) {
		comments { nodes { id body createdAt user { name } } }
	}
}`

const addCommentMutation = `mutation($issueId: String!, $body: String!) {
	commentCreate(input: { issueId: $issueId, body: $body }) {
		success
		comment { id body createdAt user { name } }
	}
}`

const getTeamStatesQuery = `query($key: String!) {
	teams(filter: { key: { eq: $key } }) {
		nodes { id states { nodes { id name type } } }
	}
}`

const issueUpdateMutation = `mutation($id: String!, $input: IssueUpdateInput!) {
	issueUpdate(id: $id, input: $input) { success }
}`

const viewerQuery = `{ viewer { id } }`

const deleteIssueMutation = `mutation($id: String!) {
	issueDelete(id: $id) { success }
}`

const createIssueMutation = `mutation($teamId: String!, $title: String!, $description: String, $projectId: String, $parentId: String) {
	issueCreate(input: { teamId: $teamId, title: $title, description: $description, projectId: $projectId, parentId: $parentId }) {
		success
		issue { identifier url title description }
	}
}`

// Client is a Linear GraphQL API client that implements tracker.Provider.
type Client struct {
	api *apiclient.Client
}

// New creates a Linear client with the given base URL and API key.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.HeaderAuth("Authorization", token)),
			apiclient.WithContentType("application/json"),
			apiclient.WithProviderName("linear"),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// clampFirst bounds Linear's GraphQL `first` pagination arg to its accepted
// 1..250 range; a zero or negative MaxResults falls back to a sane default.
func clampFirst(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 250:
		return 250
	default:
		return n
	}
}

// ListIssues implements tracker.Lister.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	vars := map[string]any{
		"first": clampFirst(opts.MaxResults),
	}

	var query string
	if opts.Project != "" {
		vars["teamKey"] = opts.Project
		switch {
		case !opts.UpdatedSince.IsZero() && opts.IncludeAll:
			query = listIssuesUpdatedSinceQuery
		case !opts.UpdatedSince.IsZero():
			query = listOpenIssuesUpdatedSinceQuery
		case opts.IncludeAll:
			query = listIssuesQuery
		default:
			query = listOpenIssuesQuery
		}
	} else {
		switch {
		case !opts.UpdatedSince.IsZero() && opts.IncludeAll:
			query = listAllIssuesUpdatedSinceQuery
		case !opts.UpdatedSince.IsZero():
			query = listAllOpenIssuesUpdatedSinceQuery
		case opts.IncludeAll:
			query = listAllIssuesQuery
		default:
			query = listAllOpenIssuesQuery
		}
	}
	if !opts.UpdatedSince.IsZero() {
		vars["since"] = opts.UpdatedSince.Format(time.RFC3339)
	}

	data, err := c.doGraphQL(ctx, query, vars)
	if err != nil {
		return nil, err
	}

	var result issuesData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding issues response",
			"project", opts.Project)
	}

	issues := make([]tracker.Issue, len(result.Issues.Nodes))
	for i, li := range result.Issues.Nodes {
		project := opts.Project
		if project == "" {
			project = projectFromIdentifier(li.Identifier)
		}
		issues[i] = toTrackerIssue(li, project)
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	vars := map[string]any{"id": key}

	data, err := c.doGraphQL(ctx, getIssueQuery, vars)
	if err != nil {
		return nil, err
	}

	var result issueData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding issue response",
			"issueKey", key)
	}

	issue := toTrackerIssue(result.Issue, projectFromIdentifier(result.Issue.Identifier))
	return &issue, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	teamID, err := c.resolveTeamID(ctx, issue.Project)
	if err != nil {
		return nil, err
	}

	projectID, err := c.resolveProjectID(ctx, issue.Project)
	if err != nil {
		return nil, err
	}

	vars := map[string]any{
		"teamId": teamID,
		"title":  issue.Title,
	}
	if issue.Description != "" {
		vars["description"] = issue.Description
	}
	if projectID != "" {
		vars["projectId"] = projectID
	}
	// Linear's parentId is the parent's internal UUID, not its human-facing
	// identifier (e.g. "ENG-12"), so resolve the key the caller passed.
	if issue.ParentKey != "" {
		parentID, err := c.resolveIssueID(ctx, issue.ParentKey)
		if err != nil {
			return nil, err
		}
		vars["parentId"] = parentID
	}

	data, err := c.doGraphQL(ctx, createIssueMutation, vars)
	if err != nil {
		return nil, err
	}

	var result issueCreateData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding create response",
			"project", issue.Project)
	}

	if !result.IssueCreate.Success {
		return nil, errors.WithDetails("linear issue creation failed",
			"project", issue.Project)
	}

	created := toTrackerIssue(result.IssueCreate.Issue, issue.Project)
	return &created, nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	issueID, err := c.resolveIssueID(ctx, issueKey)
	if err != nil {
		return nil, err
	}

	vars := map[string]any{
		"issueId": issueID,
		"body":    body,
	}

	data, err := c.doGraphQL(ctx, addCommentMutation, vars)
	if err != nil {
		return nil, err
	}

	var result commentCreateData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding comment create response",
			"issueKey", issueKey)
	}

	if !result.CommentCreate.Success {
		return nil, errors.WithDetails("linear comment creation failed",
			"issueKey", issueKey)
	}

	return toTrackerComment(result.CommentCreate.Comment)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	vars := map[string]any{"id": issueKey}

	data, err := c.doGraphQL(ctx, listCommentsQuery, vars)
	if err != nil {
		return nil, err
	}

	var result issueCommentsData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding comments response",
			"issueKey", issueKey)
	}

	comments := make([]tracker.Comment, 0, len(result.Issue.Comments.Nodes))
	for _, lc := range result.Issue.Comments.Nodes {
		c, err := toTrackerComment(lc)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *c)
	}
	return comments, nil
}

// fetchTeamStates returns the workflow states for the team derived from the issue key.
func (c *Client) fetchTeamStates(ctx context.Context, key string) ([]linearState, error) {
	teamKey := projectFromIdentifier(key)
	if teamKey == "" {
		return nil, errors.WithDetails("cannot determine team from issue key", "issueKey", key)
	}

	data, err := c.doGraphQL(ctx, getTeamStatesQuery, map[string]any{"key": teamKey})
	if err != nil {
		return nil, err
	}

	var result teamStatesData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding team states response", "issueKey", key)
	}

	if len(result.Teams.Nodes) == 0 {
		return nil, errors.WithDetails("team not found", "teamKey", teamKey)
	}

	return result.Teams.Nodes[0].States.Nodes, nil
}

// linearStateType maps Linear's internal state types to a normalised tracker.Category.
func linearStateType(t string) tracker.Category {
	switch t {
	case "unstarted", "backlog", "triage":
		return tracker.CategoryUnstarted
	case "started":
		return tracker.CategoryStarted
	case "completed":
		return tracker.CategoryDone
	case "canceled":
		return tracker.CategoryClosed
	default:
		return tracker.CategoryUnknown
	}
}

// ListStatuses implements tracker.StatusLister.
func (c *Client) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	states, err := c.fetchTeamStates(ctx, key)
	if err != nil {
		return nil, err
	}

	statuses := make([]tracker.Status, len(states))
	for i, s := range states {
		statuses[i] = tracker.Status{Name: s.Name, Category: linearStateType(s.Type)}
	}
	return statuses, nil
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	issueID, err := c.resolveIssueID(ctx, key)
	if err != nil {
		return err
	}

	states, err := c.fetchTeamStates(ctx, key)
	if err != nil {
		return err
	}

	var stateID string
	var names []string
	for _, s := range states {
		names = append(names, s.Name)
		if strings.EqualFold(s.Name, targetStatus) {
			stateID = s.ID
			break
		}
	}
	if stateID == "" {
		return errors.WithDetails("target state not found",
			"issueKey", key, "targetStatus", targetStatus, "available", strings.Join(names, ", "))
	}

	updateData, err := c.doGraphQL(ctx, issueUpdateMutation, map[string]any{
		"id":    issueID,
		"input": map[string]string{"stateId": stateID},
	})
	if err != nil {
		return err
	}

	var updateResult issueUpdateData
	if err := json.Unmarshal(updateData, &updateResult); err != nil {
		return errors.WrapWithDetails(err, "decoding issue update response", "issueKey", key)
	}
	if !updateResult.IssueUpdate.Success {
		return errors.WithDetails("linear issue transition failed", "issueKey", key)
	}
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	issueID, err := c.resolveIssueID(ctx, key)
	if err != nil {
		return err
	}

	data, err := c.doGraphQL(ctx, issueUpdateMutation, map[string]any{
		"id":    issueID,
		"input": map[string]string{"assigneeId": userID},
	})
	if err != nil {
		return err
	}

	var result issueUpdateData
	if err := json.Unmarshal(data, &result); err != nil {
		return errors.WrapWithDetails(err, "decoding issue update response", "issueKey", key)
	}
	if !result.IssueUpdate.Success {
		return errors.WithDetails("linear issue assign failed", "issueKey", key)
	}
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	data, err := c.doGraphQL(ctx, viewerQuery, nil)
	if err != nil {
		return "", err
	}

	var result viewerData
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.WrapWithDetails(err, "decoding viewer response")
	}
	return result.Viewer.ID, nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	issueID, err := c.resolveIssueID(ctx, key)
	if err != nil {
		return nil, err
	}

	input := make(map[string]string)
	if opts.Title != nil {
		input["title"] = *opts.Title
	}
	if opts.Description != nil {
		input["description"] = *opts.Description
	}

	data, err := c.doGraphQL(ctx, issueUpdateMutation, map[string]any{
		"id":    issueID,
		"input": input,
	})
	if err != nil {
		return nil, err
	}

	var result issueUpdateData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "decoding issue update response", "issueKey", key)
	}
	if !result.IssueUpdate.Success {
		return nil, errors.WithDetails("linear issue edit failed", "issueKey", key)
	}

	return c.GetIssue(ctx, key)
}

// DeleteIssue implements tracker.Deleter.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	issueID, err := c.resolveIssueID(ctx, key)
	if err != nil {
		return err
	}

	vars := map[string]any{"id": issueID}

	data, err := c.doGraphQL(ctx, deleteIssueMutation, vars)
	if err != nil {
		return err
	}

	var result issueDeleteData
	if err := json.Unmarshal(data, &result); err != nil {
		return errors.WrapWithDetails(err, "decoding delete response",
			"issueKey", key)
	}

	if !result.IssueDelete.Success {
		return errors.WithDetails("linear issue deletion failed",
			"issueKey", key)
	}

	return nil
}

// resolveIssueID looks up the internal Linear issue ID for an identifier.
func (c *Client) resolveIssueID(ctx context.Context, identifier string) (string, error) {
	vars := map[string]any{"id": identifier}

	data, err := c.doGraphQL(ctx, getIssueIDQuery, vars)
	if err != nil {
		return "", err
	}

	var result issueIDData
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.WrapWithDetails(err, "decoding issue ID response",
			"identifier", identifier)
	}

	if result.Issue.ID == "" {
		return "", errors.WithDetails("linear issue not found",
			"identifier", identifier)
	}

	return result.Issue.ID, nil
}

func toTrackerComment(lc linearComment) (*tracker.Comment, error) {
	created, err := time.Parse(time.RFC3339, lc.CreatedAt)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing comment timestamp",
			"commentID", lc.ID)
	}

	author := ""
	if lc.User != nil {
		author = lc.User.Name
	}

	return &tracker.Comment{
		ID:      lc.ID,
		Author:  author,
		Body:    lc.Body,
		Created: created,
	}, nil
}

// resolveTeamID looks up the internal Linear team ID for a team key.
func (c *Client) resolveTeamID(ctx context.Context, teamKey string) (string, error) {
	vars := map[string]any{"key": teamKey}

	data, err := c.doGraphQL(ctx, getTeamIDQuery, vars)
	if err != nil {
		return "", err
	}

	var result teamsData
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.WrapWithDetails(err, "decoding teams response",
			"teamKey", teamKey)
	}

	if len(result.Teams.Nodes) == 0 {
		return "", errors.WithDetails("linear team not found",
			"teamKey", teamKey)
	}

	return result.Teams.Nodes[0].ID, nil
}

// resolveProjectID looks up the internal Linear project ID for a project name.
// Returns ("", nil) when no matching project is found (best-effort).
func (c *Client) resolveProjectID(ctx context.Context, name string) (string, error) {
	vars := map[string]any{"name": name}

	data, err := c.doGraphQL(ctx, getProjectIDQuery, vars)
	if err != nil {
		return "", err
	}

	var result projectsData
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.WrapWithDetails(err, "decoding projects response",
			"projectName", name)
	}

	if len(result.Projects.Nodes) == 0 {
		return "", nil
	}

	return result.Projects.Nodes[0].ID, nil
}

// doGraphQL posts a GraphQL query to the Linear API and returns the data field.
func (c *Client) doGraphQL(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	return c.api.DoGraphQL(ctx, "/graphql", query, variables)
}

// toTrackerIssue converts a Linear API issue to a tracker.Issue.
func toTrackerIssue(li linearIssue, project string) tracker.Issue {
	issue := tracker.Issue{
		Key:         li.Identifier,
		Project:     project,
		Title:       li.Title,
		Status:      li.State.Name,
		StatusType:  linearStateType(li.State.Type),
		Priority:    li.PriorityLabel,
		Description: li.Description,
		URL:         li.URL,
	}

	if li.UpdatedAt != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, li.UpdatedAt)
	}
	if li.Assignee != nil {
		issue.Assignee = li.Assignee.Name
	}
	if li.Creator != nil {
		issue.Reporter = li.Creator.Name
	}
	if len(li.Labels.Nodes) > 0 {
		issue.Type = li.Labels.Nodes[0].Name
		issue.Labels = make([]string, 0, len(li.Labels.Nodes))
		for _, n := range li.Labels.Nodes {
			issue.Labels = append(issue.Labels, n.Name)
		}
	}
	if li.Parent != nil {
		issue.ParentKey = li.Parent.Identifier
	}

	return issue
}

// projectFromIdentifier extracts the team key from an identifier like "ENG-123".
func projectFromIdentifier(identifier string) string {
	idx := strings.LastIndex(identifier, "-")
	if idx < 0 {
		return ""
	}
	return strings.ToUpper(identifier[:idx])
}
