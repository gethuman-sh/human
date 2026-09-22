package local

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Kind is the tracker kind this package implements.
const Kind = "local"

// DefaultPrefix is the key prefix a local tracker uses when the config names
// none: keys read LOC-1, LOC-2, … in the shape every other command already
// accepts (the Jira/Linear grammar), so nothing downstream learns a new one.
const DefaultPrefix = "LOC"

// prefixRe is the project half of the Jira/Linear key grammar. A prefix outside
// it would produce keys DetectCandidateKinds cannot see, so it is refused at
// load rather than discovered at the first `human get`.
var prefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

// ValidatePrefix reports whether a prefix yields keys the rest of the tool can
// route. "SC" is refused because SC-nnn is Shortcut's display form and a local
// tracker using it would make every Shortcut key ambiguous.
func ValidatePrefix(prefix string) error {
	if !prefixRe.MatchString(prefix) {
		return errors.WithDetails("local tracker prefix must be uppercase letters and digits, starting with a letter", "prefix", prefix)
	}
	if prefix == "SC" {
		return errors.WithDetails("local tracker prefix SC collides with Shortcut's SC-nnn keys", "prefix", prefix)
	}
	return nil
}

// statuses is the fixed workflow. Four names, one per semantic category, so
// `human done`/`human close` and the board's derivation need nothing configured.
var statuses = []tracker.Status{
	{Name: "Backlog", Category: tracker.CategoryUnstarted},
	{Name: "In Progress", Category: tracker.CategoryStarted},
	{Name: "Done", Category: tracker.CategoryDone},
	{Name: "Closed", Category: tracker.CategoryClosed},
}

var openStatuses = []string{"Backlog", "In Progress"}

func statusNamed(name string) (tracker.Status, bool) {
	for _, st := range statuses {
		if strings.EqualFold(strings.TrimSpace(name), st.Name) {
			return st, true
		}
	}
	return tracker.Status{}, false
}

func statusNames() []string {
	names := make([]string, len(statuses))
	for i, st := range statuses {
		names[i] = st.Name
	}
	return names
}

// Client is the local tracker. It implements tracker.Provider and the optional
// capabilities the daemon asks for (CurrentUserNamer, PagedLister).
type Client struct {
	st     *store
	prefix string
	user   string
	path   string
}

var (
	_ tracker.Provider         = (*Client)(nil)
	_ tracker.CurrentUserNamer = (*Client)(nil)
	_ tracker.PagedLister      = (*Client)(nil)
)

// Option customises a client at open time.
type Option func(*options)

type options struct {
	now func() time.Time
}

// WithClock injects the time source so tests can assert timestamps.
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now != nil {
			o.now = now
		}
	}
}

// Open opens (or creates) the tracker database at path. user is the name
// every write is attributed to; it is also what GetCurrentUser answers, so an
// assigned ticket renders as the viewer's own on the board.
func Open(path, prefix, user string, opts ...Option) (*Client, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	st, err := openStore(path, o.now)
	if err != nil {
		return nil, err
	}
	return &Client{st: st, prefix: prefix, user: strings.TrimSpace(user), path: path}, nil
}

// Close releases the database.
func (c *Client) Close() error { return c.st.close() }

// Prefix returns the key prefix, which doubles as the tracker's one project.
func (c *Client) Prefix() string { return c.prefix }

// Path returns where the database lives.
func (c *Client) Path() string { return c.path }

func (c *Client) key(number int64) string {
	return c.prefix + "-" + strconv.FormatInt(number, 10)
}

// parseKey accepts PREFIX-n in any letter case and nothing else: a key from a
// different tracker must fail here, never be silently read as a local number.
func (c *Client) parseKey(key string) (int64, error) {
	trimmed := strings.TrimSpace(key)
	head, tail, ok := strings.Cut(trimmed, "-")
	if !ok || !strings.EqualFold(head, c.prefix) {
		return 0, errors.WithDetails("not a key of this local tracker", "key", key, "prefix", c.prefix)
	}
	n, err := strconv.ParseInt(tail, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.WithDetails("not a key of this local tracker", "key", key, "prefix", c.prefix)
	}
	return n, nil
}

// checkProject accepts the tracker's own prefix or nothing. A local tracker is
// one project; asking it for another is a misrouted call, not an empty answer.
func (c *Client) checkProject(project string) error {
	if project == "" || strings.EqualFold(project, c.prefix) {
		return nil
	}
	return errors.WithDetails("local tracker has one project, its prefix", "project", project, "prefix", c.prefix)
}

func (c *Client) issueFrom(r row, links []link) tracker.Issue {
	st, _ := statusNamed(r.Status)
	issue := tracker.Issue{
		Key:         c.key(r.Number),
		Project:     c.prefix,
		Type:        r.Type,
		Title:       r.Title,
		Status:      r.Status,
		StatusType:  st.Category,
		Priority:    r.Priority,
		Assignee:    r.Assignee,
		Reporter:    r.Reporter,
		Description: r.Description,
		UpdatedAt:   r.UpdatedAt,
		Labels:      r.Labels,
	}
	if r.Parent > 0 {
		issue.ParentKey = c.key(r.Parent)
	}
	for _, l := range links {
		switch {
		case l.Kind == string(tracker.LinkRelated) && (l.Src == r.Number || l.Dst == r.Number):
			other := l.Dst
			if l.Src != r.Number {
				other = l.Src
			}
			issue.Links = append(issue.Links, tracker.IssueLink{Key: c.key(other), Kind: tracker.LinkRelated})
		case l.Kind == string(tracker.LinkBlocks) && l.Src == r.Number:
			issue.Links = append(issue.Links, tracker.IssueLink{Key: c.key(l.Dst), Kind: tracker.LinkBlocks})
		case l.Kind == string(tracker.LinkBlocks) && l.Dst == r.Number:
			issue.Links = append(issue.Links, tracker.IssueLink{Key: c.key(l.Src), Kind: tracker.LinkBlocks, Inbound: true})
		}
	}
	return issue
}

// ListIssues implements tracker.Lister.
func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	page, err := c.ListIssuesPage(ctx, opts)
	if err != nil {
		return nil, err
	}
	return page.Issues, nil
}

// ListIssuesPage implements tracker.PagedLister, so a capped board fetch can
// tell a missing ticket from one that fell past the cap.
func (c *Client) ListIssuesPage(ctx context.Context, opts tracker.ListOptions) (tracker.IssuePage, error) {
	if err := c.checkProject(opts.Project); err != nil {
		return tracker.IssuePage{}, err
	}
	f := listFilter{UpdatedSince: opts.UpdatedSince, Limit: opts.MaxResults}
	if !opts.IncludeAll {
		f.Statuses = openStatuses
	}
	rows, truncated, err := c.st.listRows(ctx, f)
	if err != nil {
		return tracker.IssuePage{}, err
	}
	numbers := make([]int64, len(rows))
	for i, r := range rows {
		numbers[i] = r.Number
	}
	links, err := c.st.linksTouching(ctx, numbers)
	if err != nil {
		return tracker.IssuePage{}, err
	}
	issues := make([]tracker.Issue, len(rows))
	for i, r := range rows {
		issues[i] = c.issueFrom(r, links)
	}
	return tracker.IssuePage{Issues: issues, Truncated: truncated}, nil
}

// GetIssue implements tracker.Getter.
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return nil, err
	}
	links, err := c.st.linksTouching(ctx, []int64{r.Number})
	if err != nil {
		return nil, err
	}
	issue := c.issueFrom(*r, links)
	return &issue, nil
}

func (c *Client) mustRow(ctx context.Context, key string) (*row, error) {
	number, err := c.parseKey(key)
	if err != nil {
		return nil, err
	}
	r, err := c.st.getRow(ctx, number)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.WithDetails("issue not found", "key", key)
	}
	return r, nil
}

// CreateIssue implements tracker.Creator. The project, when given, must be the
// prefix; type defaults to Task and status to Backlog.
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	if issue == nil || strings.TrimSpace(issue.Title) == "" {
		return nil, errors.WithDetails("issue title is required")
	}
	if err := c.checkProject(issue.Project); err != nil {
		return nil, err
	}
	r := row{
		Title:       strings.TrimSpace(issue.Title),
		Description: issue.Description,
		Type:        firstNonEmpty(strings.TrimSpace(issue.Type), "Task"),
		Status:      "Backlog",
		Priority:    issue.Priority,
		Assignee:    issue.Assignee,
		Reporter:    c.user,
		Labels:      issue.Labels,
	}
	if st, ok := statusNamed(issue.Status); ok {
		r.Status = st.Name
	}
	if issue.ParentKey != "" {
		parent, err := c.mustRow(ctx, issue.ParentKey)
		if err != nil {
			return nil, errors.WrapWithDetails(err, "resolving parent issue", "parent", issue.ParentKey)
		}
		r.Parent = parent.Number
	}
	number, err := c.st.insertRow(ctx, r)
	if err != nil {
		return nil, err
	}
	return c.GetIssue(ctx, c.key(number))
}

// ListComments implements tracker.Commenter.
func (c *Client) ListComments(ctx context.Context, issueKey string) ([]tracker.Comment, error) {
	r, err := c.mustRow(ctx, issueKey)
	if err != nil {
		return nil, err
	}
	rows, err := c.st.listComments(ctx, r.Number)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, len(rows))
	for i, cr := range rows {
		out[i] = tracker.Comment{ID: strconv.FormatInt(cr.ID, 10), Author: cr.Author, Body: cr.Body, Created: cr.CreatedAt}
	}
	return out, nil
}

// AddComment implements tracker.Commenter. Comments are the pipeline's whole
// protocol, so this is the write the daemon makes most.
func (c *Client) AddComment(ctx context.Context, issueKey string, body string) (*tracker.Comment, error) {
	r, err := c.mustRow(ctx, issueKey)
	if err != nil {
		return nil, err
	}
	cr, err := c.st.insertComment(ctx, r.Number, c.user, body)
	if err != nil {
		return nil, err
	}
	if err := c.st.touch(ctx, r.Number); err != nil {
		return nil, err
	}
	return &tracker.Comment{ID: strconv.FormatInt(cr.ID, 10), Author: cr.Author, Body: cr.Body, Created: cr.CreatedAt}, nil
}

// LinkIssues implements tracker.Linker. "related" is stored once for the pair;
// "blocks" is stored with the subject first, so the direction survives.
func (c *Client) LinkIssues(ctx context.Context, key string, otherKey string, kind tracker.LinkKind) error {
	a, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	b, err := c.mustRow(ctx, otherKey)
	if err != nil {
		return err
	}
	if a.Number == b.Number {
		return errors.WithDetails("an issue cannot be linked to itself", "key", key)
	}
	l := link{Src: a.Number, Dst: b.Number}
	switch kind {
	case tracker.LinkRelated:
		l.Kind = string(tracker.LinkRelated)
		if l.Src > l.Dst {
			l.Src, l.Dst = l.Dst, l.Src
		}
	case tracker.LinkBlocks:
		l.Kind = string(tracker.LinkBlocks)
	default:
		return errors.WithDetails("unsupported link kind", "kind", string(kind))
	}
	if err := c.st.insertLink(ctx, l); err != nil {
		return err
	}
	if err := c.st.touch(ctx, a.Number); err != nil {
		return err
	}
	return c.st.touch(ctx, b.Number)
}

// UnlinkIssues implements tracker.Linker.
func (c *Client) UnlinkIssues(ctx context.Context, key string, otherKey string) error {
	a, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	b, err := c.mustRow(ctx, otherKey)
	if err != nil {
		return err
	}
	if err := c.st.deleteLinks(ctx, a.Number, b.Number); err != nil {
		return err
	}
	if err := c.st.touch(ctx, a.Number); err != nil {
		return err
	}
	return c.st.touch(ctx, b.Number)
}

// DeleteIssue implements tracker.Deleter. Local data is the user's own, so a
// delete really deletes — comments and links go with it. Safe mode and the
// destructive-confirm gate sit in front of this, as they do for every backend.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	return c.st.deleteRow(ctx, r.Number)
}

// TransitionIssue implements tracker.Transitioner. Status names match
// case-insensitively; anything outside the fixed workflow is refused by name.
func (c *Client) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	st, ok := statusNamed(targetStatus)
	if !ok {
		return errors.WithDetails("unknown status for local tracker", "status", targetStatus,
			"available", strings.Join(statusNames(), ", "))
	}
	r.Status = st.Name
	return c.st.updateRow(ctx, *r)
}

// AssignIssue implements tracker.Assigner. An empty userID unassigns.
func (c *Client) AssignIssue(ctx context.Context, key string, userID string) error {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return err
	}
	r.Assignee = strings.TrimSpace(userID)
	return c.st.updateRow(ctx, *r)
}

// GetCurrentUser implements tracker.CurrentUserGetter.
func (c *Client) GetCurrentUser(context.Context) (string, error) {
	return c.user, nil
}

// CurrentUserName implements tracker.CurrentUserNamer; the id and the display
// name are the same thing here.
func (c *Client) CurrentUserName(context.Context) (string, error) {
	return c.user, nil
}

// EditIssue implements tracker.Editor.
func (c *Client) EditIssue(ctx context.Context, key string, opts tracker.EditOptions) (*tracker.Issue, error) {
	r, err := c.mustRow(ctx, key)
	if err != nil {
		return nil, err
	}
	if opts.Title != nil {
		if strings.TrimSpace(*opts.Title) == "" {
			return nil, errors.WithDetails("issue title cannot be empty", "key", key)
		}
		r.Title = strings.TrimSpace(*opts.Title)
	}
	if opts.Description != nil {
		r.Description = *opts.Description
	}
	if opts.Type != nil && strings.TrimSpace(*opts.Type) != "" {
		r.Type = strings.TrimSpace(*opts.Type)
	}
	r.Labels = editLabels(r.Labels, opts.AddLabels, opts.RemoveLabels)
	if err := c.st.updateRow(ctx, *r); err != nil {
		return nil, err
	}
	return c.GetIssue(ctx, key)
}

// editLabels applies removals then additions, keeps order, and never stores a
// label twice: `human idea promote` removes and re-runs must be idempotent.
func editLabels(current, add, remove []string) []string {
	out := make([]string, 0, len(current)+len(add))
	for _, l := range current {
		if !containsFold(remove, l) && !containsFold(out, l) {
			out = append(out, l)
		}
	}
	for _, l := range add {
		l = strings.TrimSpace(l)
		if l != "" && !containsFold(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, l := range list {
		if strings.EqualFold(strings.TrimSpace(l), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

// ListStatuses implements tracker.StatusLister. The workflow is fixed, so the
// key is only checked for shape.
func (c *Client) ListStatuses(_ context.Context, key string) ([]tracker.Status, error) {
	if key != "" {
		if _, err := c.parseKey(key); err != nil {
			return nil, err
		}
	}
	out := make([]tracker.Status, len(statuses))
	copy(out, statuses)
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
