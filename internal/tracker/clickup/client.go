package clickup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/tracker"
)

var _ tracker.Provider = (*Client)(nil)

// Client is a ClickUp REST API client that implements tracker.Provider.
type Client struct {
	api    *apiclient.Client
	teamID string // workspace ID for custom task ID resolution

	statusesMu    sync.Mutex
	statusesCache map[string][]cuStatus // list ID -> statuses

	membersMu sync.Mutex
	members   map[int64]string // user ID -> display name
}

// New creates a ClickUp client with the given base URL, API token, and optional team ID.
func New(baseURL, token string, teamID string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.HeaderAuth("Authorization", token)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithProviderName("clickup"),
		),
		teamID:        teamID,
		statusesCache: make(map[string][]cuStatus),
		members:       make(map[int64]string),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListIssues implements tracker.Lister using GET /api/v2/list/{list_id}/task with pagination.
// Requires opts.Project to be set (= ClickUp list ID).
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	listID := opts.Project
	if listID == "" {
		return nil, errors.WithDetails("project (list ID) is required for ClickUp")
	}

	var allTasks []cuTask
	page := 0
	for {
		path := fmt.Sprintf("/api/v2/list/%s/task", url.PathEscape(listID))
		query := fmt.Sprintf("page=%d", page)
		if !opts.UpdatedSince.IsZero() {
			query += fmt.Sprintf("&date_updated_gt=%d", opts.UpdatedSince.UnixMilli())
		}

		resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
		if err != nil {
			return nil, err
		}
		var taskResp cuTaskListResponse
		if err := apiclient.DecodeJSON(resp, &taskResp, "listID", listID); err != nil {
			return nil, err
		}
		allTasks = append(allTasks, taskResp.Tasks...)
		if taskResp.LastPage {
			break
		}
		page++
	}

	issues := make([]tracker.Issue, 0, len(allTasks))
	for _, task := range allTasks {
		if !opts.IncludeAll && isTaskDone(task) {
			continue
		}
		issue := c.toTrackerIssue(ctx, task)
		issues = append(issues, issue)
	}
	return issues, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)

	resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task, "key", key); err != nil {
		return nil, err
	}

	issue := c.toTrackerIssue(ctx, task)
	return &issue, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	listID := issue.Project
	if listID == "" {
		return nil, errors.WithDetails("project (list ID) is required for ClickUp create")
	}

	body := map[string]any{
		"name": issue.Title,
	}
	if issue.Description != "" {
		body["description"] = issue.Description
	}
	if issue.ParentKey != "" {
		body["parent"] = issue.ParentKey
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request")
	}

	path := fmt.Sprintf("/api/v2/list/%s/task", url.PathEscape(listID))
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task); err != nil {
		return nil, err
	}

	return &tracker.Issue{
		Key:         task.ID,
		Project:     listID,
		Title:       task.Name,
		Description: task.Description,
		URL:         task.URL,
	}, nil
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	path := fmt.Sprintf("/api/v2/task/%s/comment", url.PathEscape(issueKey))
	query := c.customIDQuery(issueKey)

	resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, err
	}
	var commentResp cuCommentListResponse
	if err := apiclient.DecodeJSON(resp, &commentResp, "issueKey", issueKey); err != nil {
		return nil, err
	}

	result := make([]tracker.Comment, 0, len(commentResp.Comments))
	for _, cc := range commentResp.Comments {
		result = append(result, toTrackerComment(cc))
	}
	return result, nil
}

// AddComment implements tracker.Commenter.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	payload, err := json.Marshal(map[string]string{"comment_text": body})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling comment request", "issueKey", issueKey)
	}

	path := fmt.Sprintf("/api/v2/task/%s/comment", url.PathEscape(issueKey))
	query := c.customIDQuery(issueKey)
	resp, err := c.doRequest(ctx, http.MethodPost, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	// ClickUp returns the created comment directly (not wrapped).
	var cc cuComment
	if err := apiclient.DecodeJSON(resp, &cc, "issueKey", issueKey); err != nil {
		return nil, err
	}

	tc := toTrackerComment(cc)
	return &tc, nil
}

// DeleteIssue implements tracker.Deleter.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)

	resp, err := c.doRequest(ctx, http.MethodDelete, path, query, nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// TransitionIssue implements tracker.Transitioner.
// ClickUp accepts the status name directly (no ID resolution needed).
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	payload, err := json.Marshal(map[string]string{"status": targetStatus})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "key", key)
	}

	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodPut, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return errors.WithDetails("invalid ClickUp user ID, expected numeric", "userID", userID)
	}

	payload, err := json.Marshal(map[string]any{
		"assignees": map[string]any{
			"add": []int64{uid},
			"rem": []int64{},
		},
	})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "key", key)
	}

	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodPut, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v2/user", "", nil, "")
	if err != nil {
		return "", err
	}
	var cu cuCurrentUser
	if err := apiclient.DecodeJSON(resp, &cu); err != nil {
		return "", err
	}
	return strconv.FormatInt(cu.User.ID, 10), nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	fields := make(map[string]string)
	if opts.Title != nil {
		fields["name"] = *opts.Title
	}
	if opts.Description != nil {
		fields["description"] = *opts.Description
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "key", key)
	}

	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodPut, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task, "key", key); err != nil {
		return nil, err
	}

	issue := c.toTrackerIssue(ctx, task)
	return &issue, nil
}

// ListStatuses implements tracker.StatusLister.
// Fetches the task to find its list ID, then fetches the list to get statuses.
func (c *Client) ListStatuses(ctx context.Context, key string) ([]tracker.Status, error) {
	// First, get the task to find its list ID.
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task, "key", key); err != nil {
		return nil, err
	}

	listID := task.List.ID
	statuses, err := c.fetchListStatuses(ctx, listID)
	if err != nil {
		return nil, err
	}

	result := make([]tracker.Status, 0, len(statuses))
	for _, s := range statuses {
		result = append(result, tracker.Status{
			Name:     s.Status,
			Category: mapStatusType(s.Type),
		})
	}
	return result, nil
}

// fetchListStatuses fetches and caches statuses for a given list ID.
func (c *Client) fetchListStatuses(ctx context.Context, listID string) ([]cuStatus, error) {
	c.statusesMu.Lock()
	if cached, ok := c.statusesCache[listID]; ok {
		c.statusesMu.Unlock()
		return cached, nil
	}
	c.statusesMu.Unlock()

	path := fmt.Sprintf("/api/v2/list/%s", url.PathEscape(listID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "fetching list details", "listID", listID)
	}
	var list cuListDetail
	if err := apiclient.DecodeJSON(resp, &list); err != nil {
		return nil, err
	}

	c.statusesMu.Lock()
	// Double-check: another goroutine may have populated the cache.
	if cached, ok := c.statusesCache[listID]; ok {
		c.statusesMu.Unlock()
		return cached, nil
	}
	c.statusesCache[listID] = list.Statuses
	c.statusesMu.Unlock()

	return list.Statuses, nil
}

func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string, body io.Reader, contentType string) (*http.Response, error) {
	if contentType != "" {
		return c.api.DoWithContentType(ctx, method, path, rawQuery, body, contentType)
	}
	return c.api.Do(ctx, method, path, rawQuery, body)
}

// customIDQuery returns query parameters for custom task ID resolution.
// When teamID is set and the key looks like a custom ID (contains a hyphen
// with an uppercase prefix), it returns the query params for custom_task_ids.
// teamID comes from .humanconfig and may contain characters that break
// query encoding, so it is escaped through url.Values like every other
// query site in this file.
func (c *Client) customIDQuery(key string) string {
	if c.teamID == "" {
		return ""
	}
	if looksLikeCustomID(key) {
		v := url.Values{}
		v.Set("custom_task_ids", "true")
		v.Set("team_id", c.teamID)
		return v.Encode()
	}
	return ""
}

// looksLikeCustomID returns true if the key matches the pattern of a custom
// task ID like "PREFIX-123" (uppercase prefix, hyphen, digits).
func looksLikeCustomID(key string) bool {
	for i, ch := range key {
		if ch == '-' && i > 0 {
			// Check that everything before hyphen is uppercase alpha
			// and everything after is digits.
			prefix := key[:i]
			suffix := key[i+1:]
			if suffix == "" {
				return false
			}
			allUpper := true
			for _, c := range prefix {
				if c < 'A' || c > 'Z' {
					allUpper = false
					break
				}
			}
			allDigits := true
			for _, c := range suffix {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			return allUpper && allDigits
		}
	}
	return false
}

// toTrackerIssue converts a ClickUp task to a tracker.Issue.
func (c *Client) toTrackerIssue(ctx context.Context, task cuTask) tracker.Issue {
	assignee := ""
	if len(task.Assignees) > 0 {
		assignee = c.resolveMemberName(ctx, task.Assignees[0])
	}

	creator := c.resolveMemberName(ctx, task.Creator)

	priority := ""
	if task.Priority != nil {
		priority = task.Priority.Priority
	}

	var labels []string
	for _, tag := range task.Tags {
		labels = append(labels, tag.Name)
	}

	issue := tracker.Issue{
		Key:         task.ID,
		Title:       task.Name,
		Status:      task.Status.Status,
		StatusType:  mapStatusType(task.Status.Type),
		Assignee:    assignee,
		Reporter:    creator,
		Priority:    priority,
		Description: task.Description,
		URL:         task.URL,
		ParentKey:   task.Parent,
		Labels:      labels,
	}
	if task.DateUpdated != "" {
		issue.UpdatedAt = parseUnixMs(task.DateUpdated)
	}
	return issue
}

// toTrackerComment converts a ClickUp comment to a tracker.Comment.
func toTrackerComment(cc cuComment) tracker.Comment {
	author := cc.User.Username
	if author == "" {
		author = cc.User.Email
	}

	return tracker.Comment{
		ID:      cc.ID,
		Author:  author,
		Body:    cc.CommentText,
		Created: parseUnixMs(cc.Date),
	}
}

// resolveMemberName returns a display name for a user, caching results.
func (c *Client) resolveMemberName(_ context.Context, user cuUser) string {
	if user.ID == 0 {
		return ""
	}

	c.membersMu.Lock()
	if name, ok := c.members[user.ID]; ok {
		c.membersMu.Unlock()
		return name
	}
	c.membersMu.Unlock()

	// Use the username from the embedded user object.
	name := user.Username
	if name == "" {
		name = user.Email
	}
	if name == "" {
		name = user.Initials
	}

	c.membersMu.Lock()
	if cached, ok := c.members[user.ID]; ok {
		c.membersMu.Unlock()
		return cached
	}
	c.members[user.ID] = name
	c.membersMu.Unlock()

	return name
}

// mapStatusType maps a ClickUp status type to a tracker.Category.
func mapStatusType(cuType string) tracker.Category {
	switch cuType {
	case "open":
		return tracker.CategoryUnstarted
	case "custom":
		return tracker.CategoryStarted
	case "done":
		return tracker.CategoryDone
	case "closed":
		return tracker.CategoryDone
	default:
		return tracker.CategoryUnknown
	}
}

// isTaskDone returns true once a task sits in a terminal ClickUp category
// (either "done" or "closed"), so scheduled/sync flows can skip it.
func isTaskDone(task cuTask) bool {
	return task.Status.Type == "done" || task.Status.Type == "closed"
}

// parseUnixMs parses a ClickUp timestamp (unix milliseconds as string) into time.Time.
func parseUnixMs(s string) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// --- Hierarchy browsing (ClickUp-specific, not part of tracker.Provider) ---

// TeamID returns the configured workspace/team ID.
func (c *Client) TeamID() string {
	return c.teamID
}

// ListSpaces lists all spaces in the workspace identified by teamID.
func (c *Client) ListSpaces(ctx context.Context, teamID string) ([]Space, error) {
	path := fmt.Sprintf("/api/v2/team/%s/space", url.PathEscape(teamID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var spacesResp cuSpacesResponse
	if err := apiclient.DecodeJSON(resp, &spacesResp); err != nil {
		return nil, err
	}
	result := make([]Space, 0, len(spacesResp.Spaces))
	for _, s := range spacesResp.Spaces {
		result = append(result, Space(s))
	}
	return result, nil
}

// ListFolders lists all folders in the given space.
func (c *Client) ListFolders(ctx context.Context, spaceID string) ([]Folder, error) {
	path := fmt.Sprintf("/api/v2/space/%s/folder", url.PathEscape(spaceID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var foldersResp cuFoldersResponse
	if err := apiclient.DecodeJSON(resp, &foldersResp); err != nil {
		return nil, err
	}
	result := make([]Folder, 0, len(foldersResp.Folders))
	for _, f := range foldersResp.Folders {
		result = append(result, Folder(f))
	}
	return result, nil
}

// ListLists lists all lists in the given folder.
func (c *Client) ListLists(ctx context.Context, folderID string) ([]List, error) {
	path := fmt.Sprintf("/api/v2/folder/%s/list", url.PathEscape(folderID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var listsResp cuListsResponse
	if err := apiclient.DecodeJSON(resp, &listsResp); err != nil {
		return nil, err
	}
	result := make([]List, 0, len(listsResp.Lists))
	for _, l := range listsResp.Lists {
		result = append(result, List(l))
	}
	return result, nil
}

// ListFolderlessLists lists all lists directly under a space (not inside a folder).
func (c *Client) ListFolderlessLists(ctx context.Context, spaceID string) ([]List, error) {
	path := fmt.Sprintf("/api/v2/space/%s/list", url.PathEscape(spaceID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var listsResp cuListsResponse
	if err := apiclient.DecodeJSON(resp, &listsResp); err != nil {
		return nil, err
	}
	result := make([]List, 0, len(listsResp.Lists))
	for _, l := range listsResp.Lists {
		result = append(result, List(l))
	}
	return result, nil
}

// --- Members (ClickUp-specific) ---

// ListWorkspaceMembers lists all members of the workspace.
func (c *Client) ListWorkspaceMembers(ctx context.Context, teamID string) ([]Member, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v2/team", "", nil, "")
	if err != nil {
		return nil, err
	}
	var teamsResp cuTeamsResponse
	if err := apiclient.DecodeJSON(resp, &teamsResp); err != nil {
		return nil, err
	}
	for _, team := range teamsResp.Teams {
		if team.ID == teamID {
			result := make([]Member, 0, len(team.Members))
			for _, m := range team.Members {
				result = append(result, Member{
					ID:       m.User.ID,
					Username: m.User.Username,
					Email:    m.User.Email,
				})
			}
			return result, nil
		}
	}
	return nil, errors.WithDetails("workspace not found", "teamID", teamID)
}

// --- Custom fields (ClickUp-specific) ---

// GetCustomFields returns the custom field values on a task.
func (c *Client) GetCustomFields(ctx context.Context, key string) ([]CustomFieldValue, error) {
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task, "key", key); err != nil {
		return nil, err
	}
	result := make([]CustomFieldValue, 0, len(task.CustomFields))
	for _, cf := range task.CustomFields {
		result = append(result, CustomFieldValue(cf))
	}
	return result, nil
}

// SetCustomField sets a custom field value on a task.
func (c *Client) SetCustomField(ctx context.Context, taskID, fieldID string, value any) error {
	payload, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling custom field request", "taskID", taskID, "fieldID", fieldID)
	}
	path := fmt.Sprintf("/api/v2/task/%s/field/%s", url.PathEscape(taskID), url.PathEscape(fieldID))
	query := c.customIDQuery(taskID)
	resp, err := c.doRequest(ctx, http.MethodPost, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// --- Markdown description (ClickUp-specific) ---

// GetMarkdownDescription fetches the task's markdown description source.
func (c *Client) GetMarkdownDescription(ctx context.Context, key string) (string, error) {
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := "include_markdown_description=true"
	if q := c.customIDQuery(key); q != "" {
		query += "&" + q
	}
	resp, err := c.doRequest(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return "", err
	}
	var task cuTask
	if err := apiclient.DecodeJSON(resp, &task, "key", key); err != nil {
		return "", err
	}
	return task.MarkdownDescription, nil
}

// SetMarkdownDescription updates the task's markdown description.
func (c *Client) SetMarkdownDescription(ctx context.Context, key string, markdown string) error {
	payload, err := json.Marshal(map[string]string{"markdown_description": markdown})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling markdown description", "key", key)
	}
	path := fmt.Sprintf("/api/v2/task/%s", url.PathEscape(key))
	query := c.customIDQuery(key)
	resp, err := c.doRequest(ctx, http.MethodPut, path, query, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
