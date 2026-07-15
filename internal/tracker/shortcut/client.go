package shortcut

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/tracker"
)

var _ tracker.Provider = (*Client)(nil)

// Client is a Shortcut REST API client that implements tracker.Provider.
type Client struct {
	api *apiclient.Client

	statesMu       sync.Mutex
	states         map[int64]string           // workflow_state_id → state name
	stateTypes     map[int64]tracker.Category // workflow_state_id → normalised category
	defaultStateID int64                      // first Unstarted state (for creating stories)

	membersMu sync.Mutex
	members   map[string]string // member UUID → display name

	groupsMu sync.Mutex
	groups   map[string]string // group name → group UUID
}

// New creates a Shortcut client with the given base URL and API token.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.HeaderAuth("Shortcut-Token", token)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithProviderName("shortcut"),
		),
		members: make(map[string]string),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// ListIssues implements tracker.Lister using GET /api/v3/groups/{id}/stories
// for full sync, or POST /api/v3/stories/search for incremental sync.
// When opts.Project is empty, searches across all groups.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	project := opts.Project

	var stories []scStory
	var err error

	if project != "" {
		groupID, gErr := c.resolveGroupID(ctx, project)
		if gErr != nil {
			return nil, gErr
		}
		if groupID == "" {
			return nil, errors.WithDetails("group not found in Shortcut", "project", project)
		}
		if !opts.UpdatedSince.IsZero() {
			stories, err = c.searchStories(ctx, groupID, opts.UpdatedSince)
		} else {
			stories, err = c.listGroupStories(ctx, groupID)
		}
	} else {
		// Use search for all stories regardless of team assignment.
		// listAllGroupStories only returns stories with a group_id set, so
		// stories with no team are silently dropped. searchAllStories with
		// {"archived":false} returns everything and avoids the empty-body
		// problem seen on some Shortcut workspaces.
		stories, err = c.searchAllStories(ctx, opts.UpdatedSince)
	}
	if err != nil {
		return nil, err
	}

	// Pre-load group name map for resolving story group IDs.
	if project == "" {
		if _, gErr := c.resolveGroupID(ctx, ""); gErr != nil {
			return nil, gErr
		}
	}

	issues := make([]tracker.Issue, 0, len(stories))
	for _, story := range stories {
		p := project
		if p == "" {
			p = c.groupNameByID(story.GroupID)
		}
		issue, cErr := c.toTrackerIssue(ctx, story, p)
		if cErr != nil {
			return nil, cErr
		}
		if !opts.IncludeAll && c.isDoneOrArchived(story) {
			continue
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// listGroupStories fetches all stories for a group via the group endpoint.
func (c *Client) listGroupStories(ctx context.Context, groupID string) ([]scStory, error) {
	path := fmt.Sprintf("/api/v3/groups/%s/stories", url.PathEscape(groupID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var stories []scStory
	if err := apiclient.DecodeJSON(resp, &stories, "groupID", groupID); err != nil {
		return nil, err
	}
	return stories, nil
}

// searchStories uses POST /api/v3/stories/search with updated_at_start filter.
func (c *Client) searchStories(ctx context.Context, groupID string, since time.Time) ([]scStory, error) {
	body := scSearchRequest{
		GroupIDs:       []string{groupID},
		UpdatedAtStart: since.Format(time.RFC3339),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling search request")
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/stories/search", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var stories []scStory
	if err := apiclient.DecodeJSON(resp, &stories); err != nil {
		return nil, err
	}
	return stories, nil
}

// searchAllStories searches across all groups, optionally filtering by updated time.
// Archived is always set to false so the request body is never empty — sending {}
// returns no results on some Shortcut workspaces, while {"archived":false} returns
// all non-archived stories regardless of team assignment.
func (c *Client) searchAllStories(ctx context.Context, since time.Time) ([]scStory, error) {
	archived := false
	body := scSearchRequest{Archived: &archived}
	if !since.IsZero() {
		body.UpdatedAtStart = since.Format(time.RFC3339)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling search request")
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/stories/search", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var stories []scStory
	if err := apiclient.DecodeJSON(resp, &stories); err != nil {
		return nil, err
	}
	return stories, nil
}

// groupNameByID returns the group name for a UUID, or "" if not found.
// Requires resolveGroupID to have been called first to populate the cache.
func (c *Client) groupNameByID(id string) string {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()
	for name, gid := range c.groups {
		if gid == id {
			return name
		}
	}
	return ""
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	id, err := parseStoryID(key)
	if err != nil {
		return nil, err
	}

	story, err := c.fetchStory(ctx, id)
	if err != nil {
		return nil, err
	}

	issue, err := c.toTrackerIssue(ctx, *story, "")
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// fetchStory retrieves a single story by numeric ID. Shared by GetIssue and
// label edits, which must read the current label set before writing.
func (c *Client) fetchStory(ctx context.Context, id int64) (*scStory, error) {
	path := fmt.Sprintf("/api/v3/stories/%d", id)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var story scStory
	if err := apiclient.DecodeJSON(resp, &story, "storyID", id); err != nil {
		return nil, err
	}
	return &story, nil
}

// CreateIssue implements tracker.Creator.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	body := map[string]any{
		"name": issue.Title,
	}
	if issue.Description != "" {
		body["description"] = issue.Description
	}
	if len(issue.Labels) > 0 {
		body["labels"] = scLabelParams(issue.Labels)
	}
	if isValidStoryType(issue.Type) {
		body["story_type"] = issue.Type
	}
	// A subtask in Shortcut is a story that points at a parent story; the
	// key the caller passes is the numeric parent story ID.
	if issue.ParentKey != "" {
		parentID, err := parseStoryID(issue.ParentKey)
		if err != nil {
			return nil, errors.WrapWithDetails(err, "invalid parent story ID", "parentKey", issue.ParentKey)
		}
		body["parent_story_id"] = parentID
	}

	stateID, err := c.defaultWorkflowStateID(ctx)
	if err != nil {
		return nil, err
	}
	body["workflow_state_id"] = stateID

	if issue.Project != "" {
		groupID, err := c.resolveGroupID(ctx, issue.Project)
		if err != nil {
			return nil, err
		}
		if groupID != "" {
			body["group_id"] = groupID
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling create request")
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/stories", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var story scStory
	if err := apiclient.DecodeJSON(resp, &story); err != nil {
		return nil, err
	}

	return &tracker.Issue{
		Key:         strconv.FormatInt(story.ID, 10),
		Project:     issue.Project,
		Title:       story.Name,
		Description: story.Description,
		Type:        story.StoryType,
		URL:         story.AppURL,
		ParentKey:   parentStoryKey(story.ParentStoryID),
		Labels:      labelNames(story.Labels),
	}, nil
}

// ListStatuses implements tracker.StatusLister.
func (c *Client) ListStatuses(ctx context.Context, _ string) ([]tracker.Status, error) {
	c.statesMu.Lock()
	defer c.statesMu.Unlock()

	if c.states == nil {
		if err := c.fetchWorkflowsLocked(ctx); err != nil {
			return nil, err
		}
	}

	statuses := make([]tracker.Status, 0, len(c.states))
	for id, name := range c.states {
		statuses = append(statuses, tracker.Status{
			Name:     name,
			Category: c.stateTypes[id],
		})
	}
	slices.SortFunc(statuses, func(a, b tracker.Status) int {
		return strings.Compare(a.Name, b.Name)
	})
	return statuses, nil
}

// resolveStateByName matches a target status name against cached workflow states.
// Returns the state ID or an error listing available state names.
func (c *Client) resolveStateByName(ctx context.Context, targetStatus string) (int64, error) {
	c.statesMu.Lock()
	defer c.statesMu.Unlock()

	if c.states == nil {
		if err := c.fetchWorkflowsLocked(ctx); err != nil {
			return 0, err
		}
	}

	// Try exact name match (case-insensitive).
	for id, name := range c.states {
		if strings.EqualFold(name, targetStatus) {
			return id, nil
		}
	}

	// Fall back to type-based match for backward compat with "issue start".
	targetLower := tracker.Category(strings.ToLower(targetStatus))
	for id, typ := range c.stateTypes {
		if typ == targetLower {
			return id, nil
		}
	}

	names := make([]string, 0, len(c.states))
	for _, name := range c.states {
		names = append(names, name)
	}
	return 0, errors.WithDetails("workflow state not found",
		"targetStatus", targetStatus, "available", strings.Join(names, ", "))
}

// TransitionIssue implements tracker.Transitioner.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	id, err := parseStoryID(key)
	if err != nil {
		return err
	}

	stateID, err := c.resolveStateByName(ctx, targetStatus)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]int64{"workflow_state_id": stateID})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling transition request", "key", key)
	}

	path := fmt.Sprintf("/api/v3/stories/%d", id)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AssignIssue implements tracker.Assigner.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	id, err := parseStoryID(key)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string][]string{"owner_ids": {userID}})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling assign request", "key", key)
	}

	path := fmt.Sprintf("/api/v3/stories/%d", id)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v3/member-info", "", nil, "")
	if err != nil {
		return "", err
	}
	var info scMemberInfo
	if err := apiclient.DecodeJSON(resp, &info); err != nil {
		return "", err
	}
	return info.ID, nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	id, err := parseStoryID(key)
	if err != nil {
		return nil, err
	}

	fields := make(map[string]any)
	if opts.Title != nil {
		fields["name"] = *opts.Title
	}
	if opts.Description != nil {
		fields["description"] = *opts.Description
	}
	// Shortcut's story update replaces the full label set, so the current
	// labels must be fetched and merged before writing. Labels stay out of
	// the body entirely when the edit doesn't touch them, keeping
	// title/description-only edits from clobbering labels.
	if len(opts.AddLabels) > 0 || len(opts.RemoveLabels) > 0 {
		merged, mErr := c.mergedStoryLabels(ctx, id, opts)
		if mErr != nil {
			return nil, mErr
		}
		fields["labels"] = scLabelParams(merged)
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling edit request", "key", key)
	}

	path := fmt.Sprintf("/api/v3/stories/%d", id)
	resp, err := c.doRequest(ctx, http.MethodPut, path, "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var story scStory
	if err := apiclient.DecodeJSON(resp, &story, "key", key); err != nil {
		return nil, err
	}

	issue, err := c.toTrackerIssue(ctx, story, "")
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// mergedStoryLabels reads the story's current labels and applies the
// requested additions/removals, producing the full-replacement set the
// Shortcut update endpoint expects.
func (c *Client) mergedStoryLabels(ctx context.Context, id int64, opts tracker.EditOptions) ([]string, error) {
	story, err := c.fetchStory(ctx, id)
	if err != nil {
		return nil, err
	}
	return mergeLabels(labelNames(story.Labels), opts.AddLabels, opts.RemoveLabels), nil
}

// mergeLabels applies add/remove requests to an existing label set. Existing
// order is preserved, additions are appended, duplicates are dropped, and
// removing an absent label is a no-op so label swaps stay idempotent.
func mergeLabels(existing, add, remove []string) []string {
	removed := make(map[string]bool, len(remove))
	for _, name := range remove {
		removed[name] = true
	}
	seen := make(map[string]bool, len(existing)+len(add))
	merged := make([]string, 0, len(existing)+len(add))
	keep := func(name string) {
		if removed[name] || seen[name] {
			return
		}
		seen[name] = true
		merged = append(merged, name)
	}
	for _, name := range existing {
		keep(name)
	}
	for _, name := range add {
		keep(name)
	}
	return merged
}

// labelNames flattens Shortcut label objects to their plain names.
func labelNames(labels []scLabel) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names
}

// scLabelParams renders label names as Shortcut CreateLabelParams objects;
// labels unknown to the workspace are created by Shortcut on the fly. The
// result is never nil so an emptied label set marshals as [] and actually
// clears the story's labels.
func scLabelParams(names []string) []scLabel {
	params := make([]scLabel, 0, len(names))
	for _, name := range names {
		params = append(params, scLabel{Name: name})
	}
	return params
}

// DeleteIssue implements tracker.Deleter using true deletion (DELETE /api/v3/stories/{id}).
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	id, err := parseStoryID(key)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v3/stories/%d", id)
	resp, err := c.doRequest(ctx, http.MethodDelete, path, "", nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// AddComment implements tracker.Commenter.
// LinkIssues implements tracker.Linker via Shortcut's story-link API. The
// "relates to" verb is Shortcut's symmetric story relation; subject/object
// order therefore carries no meaning beyond display.
func (c *Client) LinkIssues(ctx context.Context, key string, otherKey string) error {
	subjectID, err := parseStoryID(key)
	if err != nil {
		return err
	}
	objectID, err := parseStoryID(otherKey)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"verb":       "relates to",
		"subject_id": subjectID,
		"object_id":  objectID,
	})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling story link request", "key", key, "otherKey", otherKey)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/story-links", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return errors.WrapWithDetails(err, "linking issues", "key", key, "otherKey", otherKey)
	}
	_ = resp.Body.Close()
	return nil
}

func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	id, err := parseStoryID(issueKey)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]string{"text": body})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling comment request", "issueKey", issueKey)
	}

	path := fmt.Sprintf("/api/v3/stories/%d/comments", id)
	resp, err := c.doRequest(ctx, http.MethodPost, path, "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, err
	}
	var sc scComment
	if err := apiclient.DecodeJSON(resp, &sc, "issueKey", issueKey); err != nil {
		return nil, err
	}

	return c.toTrackerComment(ctx, sc)
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	id, err := parseStoryID(issueKey)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/api/v3/stories/%d/comments", id)
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, err
	}
	var comments []scComment
	if err := apiclient.DecodeJSON(resp, &comments, "issueKey", issueKey); err != nil {
		return nil, err
	}

	result := make([]tracker.Comment, 0, len(comments))
	for _, sc := range comments {
		tc, err := c.toTrackerComment(ctx, sc)
		if err != nil {
			return nil, err
		}
		result = append(result, *tc)
	}
	return result, nil
}

func (c *Client) doRequest(ctx context.Context, method, path, rawQuery string, body io.Reader, contentType string) (*http.Response, error) {
	if contentType != "" {
		return c.api.DoWithContentType(ctx, method, path, rawQuery, body, contentType)
	}
	return c.api.Do(ctx, method, path, rawQuery, body)
}

// resolveStateName maps a workflow_state_id to its name, fetching and caching
// workflows on first call.
func (c *Client) resolveStateName(ctx context.Context, stateID int64) (string, error) {
	c.statesMu.Lock()
	defer c.statesMu.Unlock()

	if c.states == nil {
		if err := c.fetchWorkflowsLocked(ctx); err != nil {
			return "", err
		}
	}

	if name, ok := c.states[stateID]; ok {
		return name, nil
	}
	return fmt.Sprintf("Unknown(%d)", stateID), nil
}

// fetchWorkflowsLocked fetches all workflows and populates the states cache
// and defaultStateID. Must be called with statesMu held.
func (c *Client) fetchWorkflowsLocked(ctx context.Context) error {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v3/workflows", "", nil, "")
	if err != nil {
		return errors.WrapWithDetails(err, "fetching workflows")
	}
	var workflows []scWorkflow
	if err := apiclient.DecodeJSON(resp, &workflows); err != nil {
		return err
	}

	c.states = make(map[int64]string)
	c.stateTypes = make(map[int64]tracker.Category)
	for _, wf := range workflows {
		for _, st := range wf.States {
			category := tracker.Category(st.Type)
			c.states[st.ID] = st.Name
			c.stateTypes[st.ID] = category
			if c.defaultStateID == 0 && category == tracker.CategoryUnstarted {
				c.defaultStateID = st.ID
			}
		}
	}
	return nil
}

// resolveMemberName resolves a member UUID to a display name, caching results.
func (c *Client) resolveMemberName(ctx context.Context, memberID string) (string, error) {
	if memberID == "" {
		return "", nil
	}

	c.membersMu.Lock()
	if name, ok := c.members[memberID]; ok {
		c.membersMu.Unlock()
		return name, nil
	}
	c.membersMu.Unlock()

	path := fmt.Sprintf("/api/v3/members/%s", url.PathEscape(memberID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		// Cache the empty name so a transient failure doesn't trigger a
		// re-fetch on every subsequent call for this member id.
		c.cacheMember(memberID, "")
		return "", nil
	}
	var member scMember
	if err := apiclient.DecodeJSON(resp, &member); err != nil {
		c.cacheMember(memberID, "")
		return "", nil
	}

	name := member.Profile.DisplayName
	if name == "" {
		name = member.Profile.Name
	}

	c.membersMu.Lock()
	// Double-check: another goroutine may have cached this member while we fetched.
	if cached, ok := c.members[memberID]; ok {
		c.membersMu.Unlock()
		return cached, nil
	}
	c.members[memberID] = name
	c.membersMu.Unlock()

	return name, nil
}

// cacheMember stores a (possibly empty) display name for memberID.
// Used by resolveMemberName to negative-cache transient lookup failures
// so they don't trigger a refetch on every call.
func (c *Client) cacheMember(memberID, name string) {
	c.membersMu.Lock()
	defer c.membersMu.Unlock()
	if _, ok := c.members[memberID]; ok {
		return
	}
	c.members[memberID] = name
}

// resolveGroupID maps a group name to its UUID, fetching and caching
// groups on first call. Returns empty string if the group is not found.
func (c *Client) resolveGroupID(ctx context.Context, name string) (string, error) {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()

	if c.groups != nil {
		return c.groups[name], nil
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v3/groups", "", nil, "")
	if err != nil {
		return "", errors.WrapWithDetails(err, "fetching groups")
	}
	var groups []scGroup
	if err := apiclient.DecodeJSON(resp, &groups); err != nil {
		return "", err
	}

	c.groups = make(map[string]string)
	for _, g := range groups {
		c.groups[g.Name] = g.ID
	}

	return c.groups[name], nil
}

// defaultWorkflowStateID returns the first "unstarted" workflow state ID,
// which is used as the default when creating stories. Workflows are fetched
// and cached on first call (shared with resolveStateName).
func (c *Client) defaultWorkflowStateID(ctx context.Context) (int64, error) {
	c.statesMu.Lock()
	defer c.statesMu.Unlock()

	if c.defaultStateID != 0 {
		return c.defaultStateID, nil
	}

	// If states cache is nil, we need to fetch workflows first.
	if c.states == nil {
		if err := c.fetchWorkflowsLocked(ctx); err != nil {
			return 0, err
		}
	}

	return c.defaultStateID, nil
}

// isDoneOrArchived returns true if the story is archived or in a "done" workflow state.
// Must be called after workflow states have been loaded.
func (c *Client) isDoneOrArchived(story scStory) bool {
	if story.Archived {
		return true
	}
	c.statesMu.Lock()
	stateType := c.stateTypes[story.WorkflowStateID]
	c.statesMu.Unlock()
	return stateType == tracker.CategoryDone
}

// isValidStoryType returns true if t is a Shortcut-accepted story type.
func isValidStoryType(t string) bool {
	return t == "feature" || t == "bug" || t == "chore"
}

// parseStoryID parses a string story ID into an int64.
func parseStoryID(key string) (int64, error) {
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.WithDetails("invalid story ID, expected numeric key", "key", key)
	}
	return id, nil
}

// toTrackerIssue converts a Shortcut story to a tracker.Issue.
func (c *Client) toTrackerIssue(ctx context.Context, story scStory, project string) (tracker.Issue, error) {
	stateName, err := c.resolveStateName(ctx, story.WorkflowStateID)
	if err != nil {
		return tracker.Issue{}, err
	}

	// Resolve status type from the cached state types map.
	c.statesMu.Lock()
	statusType := c.stateTypes[story.WorkflowStateID]
	c.statesMu.Unlock()

	assignee := ""
	if len(story.OwnerIDs) > 0 {
		assignee, _ = c.resolveMemberName(ctx, story.OwnerIDs[0])
	}

	reporter, _ := c.resolveMemberName(ctx, story.RequestedByID)

	issue := tracker.Issue{
		Key:         strconv.FormatInt(story.ID, 10),
		Project:     project,
		Type:        story.StoryType,
		Title:       story.Name,
		Status:      stateName,
		StatusType:  statusType,
		Assignee:    assignee,
		Reporter:    reporter,
		Description: story.Description,
		URL:         story.AppURL,
		Labels:      labelNames(story.Labels),
	}
	issue.ParentKey = parentStoryKey(story.ParentStoryID)
	if story.UpdatedAt != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, story.UpdatedAt)
	}
	return issue, nil
}

// parentStoryKey renders a parent story ID as a tracker issue key, or "" when
// the story has no parent.
func parentStoryKey(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}

// toTrackerComment converts a Shortcut comment to a tracker.Comment.
func (c *Client) toTrackerComment(ctx context.Context, sc scComment) (*tracker.Comment, error) {
	created, err := time.Parse(time.RFC3339, sc.CreatedAt)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing comment timestamp", "commentID", sc.ID)
	}

	author, _ := c.resolveMemberName(ctx, sc.AuthorID)

	return &tracker.Comment{
		ID:      strconv.FormatInt(sc.ID, 10),
		Author:  author,
		Body:    sc.Text,
		Created: created,
	}, nil
}
