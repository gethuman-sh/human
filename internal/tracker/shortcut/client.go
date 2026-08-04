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
var _ tracker.CurrentUserNamer = (*Client)(nil)

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
	page, err := c.ListIssuesPage(ctx, opts)
	return page.Issues, err
}

// ListIssuesPage implements tracker.PagedLister.
//
// Truncated is reported only when it is OBSERVED: a cursor still pending when
// collection stopped. Shortcut's older endpoints answer with a bare array and
// no cursor, and that silence is not proof of completeness — the server may
// have capped the response without saying so. Claiming "complete" there would
// be worse than admitting the limit, because the index's prune deletes whatever
// a listing omitted; the honest signal is what lets that guard work (SC-2132).
func (c *Client) ListIssuesPage(ctx context.Context, opts tracker.ListOptions) (tracker.IssuePage, error) {
	project := opts.Project

	var stories []scStory
	var truncated bool
	var err error

	if project != "" {
		groupID, gErr := c.resolveGroupID(ctx, project)
		if gErr != nil {
			return tracker.IssuePage{}, gErr
		}
		if groupID == "" {
			return tracker.IssuePage{}, errors.WithDetails("group not found in Shortcut", "project", project)
		}
		if !opts.UpdatedSince.IsZero() {
			stories, truncated, err = c.searchStories(ctx, groupID, opts.UpdatedSince)
		} else {
			stories, truncated, err = c.listGroupStories(ctx, groupID)
		}
	} else {
		// Use search for all stories regardless of team assignment.
		// listAllGroupStories only returns stories with a group_id set, so
		// stories with no team are silently dropped.
		stories, truncated, err = c.searchAllStories(ctx, opts.UpdatedSince, opts.IncludeAll)
	}
	if err != nil {
		return tracker.IssuePage{}, err
	}

	// Pre-load group name map for resolving story group IDs.
	if project == "" {
		if _, gErr := c.resolveGroupID(ctx, ""); gErr != nil {
			return tracker.IssuePage{}, gErr
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
			return tracker.IssuePage{}, cErr
		}
		if !c.belongsInResult(story, opts.IncludeAll) {
			continue
		}
		issues = append(issues, issue)
	}
	return tracker.IssuePage{Issues: issues, Truncated: truncated}, nil
}

// listGroupStories fetches all stories for a group via the group endpoint.
func (c *Client) listGroupStories(ctx context.Context, groupID string) ([]scStory, bool, error) {
	path := fmt.Sprintf("/api/v3/groups/%s/stories", url.PathEscape(groupID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, "", nil, "")
	if err != nil {
		return nil, false, err
	}
	page, err := c.decodeStories(resp)
	if err != nil {
		return nil, false, err
	}
	return c.followPages(ctx, page)
}

// searchStories uses POST /api/v3/stories/search with updated_at_start filter.
func (c *Client) searchStories(ctx context.Context, groupID string, since time.Time) ([]scStory, bool, error) {
	body := scSearchRequest{
		GroupIDs:       []string{groupID},
		UpdatedAtStart: since.Format(time.RFC3339),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, false, errors.WrapWithDetails(err, "marshalling search request")
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/stories/search", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, false, err
	}
	page, err := c.decodeStories(resp)
	if err != nil {
		return nil, false, err
	}
	return c.followPages(ctx, page)
}

// searchAllStories searches across all groups, optionally filtering by updated
// time.
//
// The archived filter has to be sent explicitly rather than omitted: an empty
// request body returns no results on some Shortcut workspaces, so {} is not a
// way to ask for "both". Asking for the record therefore issues TWO searches —
// archived and not — and merges them. Without that, an archived story could
// never be returned at all, and "was this already fixed?" would silently miss
// every ticket somebody tidied away (SC-2132).
func (c *Client) searchAllStories(ctx context.Context, since time.Time, includeArchived bool) ([]scStory, bool, error) {
	stories, truncated, err := c.searchArchived(ctx, since, false)
	if err != nil || !includeArchived {
		return stories, truncated, err
	}
	archived, archTruncated, err := c.searchArchived(ctx, since, true)
	if err != nil {
		// The unarchived half is a usable answer; losing it because the archived
		// half failed would be worse than an incomplete one — but the result is
		// then incomplete, and must say so.
		return stories, true, nil
	}
	return append(stories, archived...), truncated || archTruncated, nil
}

// searchArchived runs one archived/not-archived half of the search, following
// the cursor to the end when the response carries one.
func (c *Client) searchArchived(ctx context.Context, since time.Time, archived bool) ([]scStory, bool, error) {
	body := scSearchRequest{Archived: &archived}
	if !since.IsZero() {
		body.UpdatedAtStart = since.Format(time.RFC3339)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, false, errors.WrapWithDetails(err, "marshalling search request")
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v3/stories/search", "", bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, false, err
	}
	page, err := c.decodeStories(resp)
	if err != nil {
		return nil, false, err
	}
	return c.followPages(ctx, page)
}

// followPages walks a cursor to the end, returning everything collected and
// whether stories remain beyond what was fetched.
//
// A bare-array response has no cursor, which is NOT evidence that it was
// complete — the server may simply have capped it without saying so. That case
// reports notComplete=false because there is nothing to report from, and the
// index's prune guard is what protects against acting on a short answer. This
// function only claims truncation it can actually observe.
func (c *Client) followPages(ctx context.Context, first scStoryPage) (stories []scStory, notComplete bool, err error) {
	stories = append(stories, first.Stories...)
	next := first.Next
	for pages := 0; next != ""; pages++ {
		if pages >= maxSearchPages {
			return stories, true, nil
		}
		path, rawQuery, ok := nextPathAndQuery(next)
		if !ok {
			return stories, true, nil
		}
		resp, rErr := c.doRequest(ctx, http.MethodGet, path, rawQuery, nil, "")
		if rErr != nil {
			// Partial data plus an honest "there is more" beats losing the pages
			// already fetched.
			return stories, true, nil
		}
		page, dErr := c.decodeStories(resp)
		if dErr != nil {
			return stories, true, nil
		}
		stories = append(stories, page.Stories...)
		next = page.Next
	}
	return stories, false, nil
}

// decodeStories reads a story response in whichever shape it arrives.
func (c *Client) decodeStories(resp *http.Response) (scStoryPage, error) {
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, apiclient.MaxResponseBodyBytes+1))
	if err != nil {
		return scStoryPage{}, errors.WrapWithDetails(err, "reading story response")
	}
	return decodeStoryPage(raw)
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
	// Shortcut's native defect marker is story_type "bug"; accept any spelling
	// IsBug recognises ("Bug", "type:bug") so bug creation is tracker-agnostic
	// for callers. Other types are matched case-insensitively against the
	// valid set — Shortcut itself rejects anything but lowercase values.
	if st := normalizeStoryType(issue.Type); st != "" {
		body["story_type"] = st
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
		// A named-but-unresolved group means the caller asked for a board the
		// workspace does not have; creating group-less would land the story
		// off-board and invisible. Fail loudly, matching ListIssues.
		if groupID == "" {
			return nil, errors.WithDetails("group not found in Shortcut", "project", issue.Project)
		}
		body["group_id"] = groupID
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
		Key:         storyKey(story.ID),
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

// AssignToReporter implements tracker.ReporterAssigner: it makes the story's
// requester its owner.
//
// The join happens here because it can only happen here. A caller sees
// Issue.Reporter as a display name, and AssignIssue needs a member ID; the
// story carries both, so reading requested_by_id straight off it avoids a
// name lookup that would be ambiguous even if one existed.
//
// A story with no requester (imported or API-created without one) is left
// alone rather than assigned to nobody, which would clear an existing owner.
func (c *Client) AssignToReporter(ctx context.Context, key string) error {
	id, err := parseStoryID(key)
	if err != nil {
		return err
	}
	story, err := c.fetchStory(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(story.RequestedByID) == "" {
		return errors.WithDetails("story has no requester to assign as owner", "key", key)
	}
	return c.AssignIssue(ctx, key, story.RequestedByID)
}

// GetCurrentUser implements tracker.CurrentUserGetter. The endpoint is
// /api/v3/member ("Get Current Member Info"); /api/v3/member-info does not
// exist and answers 404, which is what silently withheld the board's viewer
// identity — and with it every ownership decision that depends on knowing who
// is looking.
func (c *Client) GetCurrentUser(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v3/member", "", nil, "")
	if err != nil {
		return "", err
	}
	var info scMemberInfo
	if err := apiclient.DecodeJSON(resp, &info); err != nil {
		return "", err
	}
	return info.ID, nil
}

// CurrentUserName implements tracker.CurrentUserNamer: the authenticated user's
// display name, resolved through the SAME member-name path that fills a story's
// Assignee/Reporter (resolveMemberName), so the board can match owner == me by
// string equality. Empty (no error) when the member has no resolvable name,
// which the board reads as "identity unknown" and dims nothing.
func (c *Client) CurrentUserName(ctx context.Context) (string, error) {
	id, err := c.GetCurrentUser(ctx)
	if err != nil {
		return "", err
	}
	return c.resolveMemberName(ctx, id)
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
	// Shortcut carries the kind natively, so a retype is one field. An
	// unrecognised type is refused rather than dropped: silently leaving the
	// story a bug while reporting the edit succeeded is the failure mode a
	// retype exists to end (SC-3051).
	if opts.Type != nil {
		st := normalizeStoryType(*opts.Type)
		if st == "" {
			return nil, errors.WithDetails("shortcut cannot express this issue type",
				"key", key, "type", *opts.Type, "known", "feature, bug, chore")
		}
		fields["story_type"] = st
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
// LinkIssues implements tracker.Linker via Shortcut's story-link API. Shortcut
// states relations as subject-verb-object, so key is the subject: with
// LinkBlocks, key blocks otherKey.
func (c *Client) LinkIssues(ctx context.Context, key string, otherKey string, kind tracker.LinkKind) error {
	verb, ok := verbFor(kind)
	if !ok {
		return errors.WithDetails("shortcut cannot express this link kind", "kind", string(kind))
	}
	subjectID, err := parseStoryID(key)
	if err != nil {
		return err
	}
	objectID, err := parseStoryID(otherKey)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"verb":       verb,
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

// verbFor maps our vocabulary onto Shortcut's. The inverse of linkKindFor, kept
// beside it so the two cannot drift.
func verbFor(kind tracker.LinkKind) (string, bool) {
	switch kind {
	case tracker.LinkBlocks:
		return "blocks", true
	case tracker.LinkRelated:
		return "relates to", true
	default:
		return "", false
	}
}

// UnlinkIssues removes the relationship between two stories, in whichever
// direction it was recorded — the caller asking to release a dependency should
// not have to know which end it was written from.
func (c *Client) UnlinkIssues(ctx context.Context, key string, otherKey string) error {
	id, err := parseStoryID(key)
	if err != nil {
		return err
	}
	otherID, err := parseStoryID(otherKey)
	if err != nil {
		return err
	}
	story, err := c.fetchStory(ctx, id)
	if err != nil {
		return err
	}
	for _, l := range story.StoryLinks {
		if (l.SubjectID == id && l.ObjectID == otherID) || (l.SubjectID == otherID && l.ObjectID == id) {
			if dErr := c.deleteStoryLink(ctx, l.ID); dErr != nil {
				return dErr
			}
		}
	}
	return nil
}

// deleteStoryLink removes one story-link record by id.
func (c *Client) deleteStoryLink(ctx context.Context, linkID int64) error {
	path := fmt.Sprintf("/api/v3/story-links/%d", linkID)
	resp, err := c.doRequest(ctx, http.MethodDelete, path, "", nil, "")
	if err != nil {
		return errors.WrapWithDetails(err, "removing story link", "linkID", linkID)
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
	return story.Archived || c.isDone(story)
}

// isDone reports whether the story reached a done workflow state. Independent of
// Archived: the two are separate fields, so a story can be archived without ever
// having been finished.
func (c *Client) isDone(story scStory) bool {
	c.statesMu.Lock()
	stateType := c.stateTypes[story.WorkflowStateID]
	c.statesMu.Unlock()
	return stateType == tracker.CategoryDone
}

// belongsInResult reports whether a story survives the caller's filter.
//
// Without IncludeAll the caller wants live work: neither finished nor put away.
//
// With IncludeAll the caller wants the record — everything that happened —
// EXCEPT work that was archived without ever being done. Archiving and
// finishing are orthogonal in Shortcut, and an archived-but-unfinished story is
// work somebody abandoned: surfacing it in a search reads as if the work exists
// or is planned, which is more misleading than not finding it at all (SC-2132).
// Archived-and-done stories are ordinary housekeeping and stay, because "was
// this already fixed?" is exactly what the record is asked.
func (c *Client) belongsInResult(story scStory, includeAll bool) bool {
	if !includeAll {
		return !c.isDoneOrArchived(story)
	}
	return !story.Archived || c.isDone(story)
}

// isValidStoryType returns true if t is a Shortcut-accepted story type.
func isValidStoryType(t string) bool {
	return t == "feature" || t == "bug" || t == "chore"
}

// normalizeStoryType maps a provider-agnostic issue type onto Shortcut's
// story_type vocabulary: bug-typed issues (any spelling tracker.IsBugType
// accepts) become "bug", other types are lowercased and kept only when
// Shortcut knows them. Empty means "omit" — Shortcut defaults to feature.
func normalizeStoryType(t string) string {
	if tracker.IsBugType(t) {
		return "bug"
	}
	if st := strings.ToLower(t); isValidStoryType(st) {
		return st
	}
	return ""
}

// keyPrefix is Shortcut's fixed universal display prefix. Shortcut has no
// per-workspace project prefix, so this is hardcoded rather than configured —
// mirroring tracker.shortcutDisplayRe, which recognises the same form.
const keyPrefix = "SC-"

// parseStoryID parses a string story ID into an int64. It accepts both the bare
// numeric key and the tool's own "SC-nnn" display form (case-insensitive), so
// every entry point that funnels through here — Get/Edit/Comment/Link — resolves
// keys copied out of commits, markers and PR titles. Keys emitted by this client
// always carry the prefix; the bare form is still accepted because it is what
// older commits, markers and stored state contain. The original key is kept in
// the error so a bad "SC-abc" still reports what the caller passed.
func parseStoryID(key string) (int64, error) {
	numeric := key
	if len(key) > len(keyPrefix) && strings.EqualFold(key[:len(keyPrefix)], keyPrefix) {
		numeric = key[len(keyPrefix):]
	}
	id, err := strconv.ParseInt(numeric, 10, 64)
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
		Key:         storyKey(story.ID),
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
	issue.Links = storyLinks(story)
	if story.UpdatedAt != "" {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, story.UpdatedAt)
	}
	return issue, nil
}

// storyLinks converts Shortcut's relationships into direction-resolved links.
//
// Shortcut states a relation as subject-verb-object — with verb "blocks",
// SubjectID blocks ObjectID — and sends the same record to both stories. Which
// end THIS story sits on is therefore decided here, by comparing its own ID,
// rather than left for callers to work out from raw identifiers. A caller that
// got that backwards would gate the wrong ticket: stalling work that was ready
// and releasing work that was not.
//
// A relation naming neither this story, or carrying a verb we do not model, is
// dropped: reporting a relationship we cannot describe is worse than reporting
// none, because the gate would act on it.
func storyLinks(story scStory) []tracker.IssueLink {
	var links []tracker.IssueLink
	for _, l := range story.StoryLinks {
		kind, ok := linkKindFor(l.Verb)
		if !ok {
			continue
		}
		switch story.ID {
		case l.SubjectID:
			links = append(links, tracker.IssueLink{Key: storyKey(l.ObjectID), Kind: kind})
		case l.ObjectID:
			links = append(links, tracker.IssueLink{Key: storyKey(l.SubjectID), Kind: kind, Inbound: true})
		}
	}
	return links
}

// linkKindFor maps Shortcut's verb vocabulary onto ours. "duplicates" is
// deliberately unmapped: it says two tickets are the same work, not that one
// waits for the other, and treating it as a blocker would stall a card behind
// its own duplicate.
func linkKindFor(verb string) (tracker.LinkKind, bool) {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "blocks":
		return tracker.LinkBlocks, true
	case "relates to":
		return tracker.LinkRelated, true
	default:
		return "", false
	}
}

// storyKey renders a story ID as this tracker's issue key: the prefixed display
// form Shortcut itself prints, which is the ONLY form the rest of the tool sees.
//
// A bare story ID was previously handed out as the key, which made a ticket's
// identity ambiguous: everything user- and agent-facing (commits, markers, the
// ticket's own commands) uses "SC-nnn", so a bare "nnn" left two spellings of
// one ticket in circulation and any handover that wrote one and read the other
// was silently lost (SC-1892). Emitting the display form here — the one place a
// story becomes an issue — leaves exactly one identity, and lets every consumer
// treat the key as an opaque string rather than guessing at its syntax.
func storyKey(id int64) string {
	return keyPrefix + strconv.FormatInt(id, 10)
}

// parentStoryKey renders a parent story ID as a tracker issue key, or "" when
// the story has no parent.
func parentStoryKey(id *int64) string {
	if id == nil {
		return ""
	}
	return storyKey(*id)
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
