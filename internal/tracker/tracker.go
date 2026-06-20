package tracker

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
)

// githubIssueRe matches GitHub issue keys like "owner/repo#123".
var githubIssueRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#\d+$`)

// githubRepoRe matches GitHub project keys like "owner/repo".
var githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// jiraLinearIssueRe matches Jira/Linear issue keys like "KAN-42" or "ENG-123".
var jiraLinearIssueRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

// numericRe matches purely numeric keys like "123" (Shortcut).
var numericRe = regexp.MustCompile(`^\d+$`)

// azureDevOpsRe matches Azure DevOps work item keys like "Project/42".
var azureDevOpsRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*/\d+$`)

// DetectCandidateKinds returns all tracker kinds whose key format matches the
// given key. The order is deterministic: azuredevops is checked before
// github/gitlab repo format since "Word/N" is a subset of "owner/repo".
func DetectCandidateKinds(key string) []string {
	if key == "" {
		return nil
	}

	var kinds []string

	if jiraLinearIssueRe.MatchString(key) {
		kinds = append(kinds, "jira", "linear")
	}

	if githubIssueRe.MatchString(key) {
		kinds = append(kinds, "github", "gitlab")
	}

	// Check azureDevOpsRe before githubRepoRe — "Project/42" matches both.
	if azureDevOpsRe.MatchString(key) {
		kinds = append(kinds, "azuredevops")
	} else if githubRepoRe.MatchString(key) {
		kinds = append(kinds, "github", "gitlab")
	}

	if numericRe.MatchString(key) {
		kinds = append(kinds, "shortcut")
	}

	return kinds
}

// ExtractProject extracts the project identifier from a key.
//
//	"KAN-42"              → "KAN"
//	"octocat/repo#42"     → "octocat/repo"
//	"octocat/repo"        → "octocat/repo"
//	"Project/42"          → "Project"
//	"123"                 → ""
func ExtractProject(key string) string {
	if idx := strings.LastIndex(key, "#"); idx >= 0 {
		return key[:idx]
	}
	if jiraLinearIssueRe.MatchString(key) {
		return key[:strings.LastIndex(key, "-")]
	}
	if azureDevOpsRe.MatchString(key) {
		return key[:strings.LastIndex(key, "/")]
	}
	if githubRepoRe.MatchString(key) {
		return key
	}
	return ""
}

// FindResult holds the outcome of FindTracker.
type FindResult struct {
	Provider string `json:"provider"`
	Project  string `json:"project"`
	Key      string `json:"key"`
}

// FindTracker determines which configured tracker owns the given key.
//
// Resolution strategy:
//  1. Match key format against regexes → candidate kinds
//  2. Filter candidates against configured instances
//  3. If one kind remains → return it (no API call)
//  4. If ambiguous → probe each instance with GetIssue until one succeeds
func FindTracker(ctx context.Context, key string, instances []Instance) (*FindResult, error) {
	candidates := DetectCandidateKinds(key)
	if len(candidates) == 0 {
		return nil, errors.WithDetails("unrecognized key format", "key", key)
	}

	// Filter to kinds that are actually configured.
	candidateSet := make(map[string]bool, len(candidates))
	for _, k := range candidates {
		candidateSet[k] = true
	}

	var matching []Instance
	seenKinds := make(map[string]bool)
	for _, inst := range instances {
		if candidateSet[inst.Kind] {
			matching = append(matching, inst)
			seenKinds[inst.Kind] = true
		}
	}

	if len(matching) == 0 {
		return nil, errors.WithDetails("no configured tracker matches key format", "key", key)
	}

	// If all matching instances are the same kind, no ambiguity.
	if len(seenKinds) == 1 {
		kind := matching[0].Kind
		return &FindResult{
			Provider: kind,
			Project:  ExtractProject(key),
			Key:      key,
		}, nil
	}

	// Ambiguous — probe each instance.
	return probeInstances(ctx, key, matching)
}

// probeTimeout is the per-provider timeout for probing instances.
const probeTimeout = 10 * time.Second

// probeInstances tries GetIssue on each instance and returns the first success.
func probeInstances(ctx context.Context, key string, instances []Instance) (*FindResult, error) {
	for _, inst := range instances {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		_, err := inst.Provider.GetIssue(probeCtx, key)
		cancel()
		if err == nil {
			return &FindResult{
				Provider: inst.Kind,
				Project:  ExtractProject(key),
				Key:      key,
			}, nil
		}
	}
	return nil, errors.WithDetails("no configured tracker recognized the key", "key", key)
}

// DetectKind returns the tracker kind that can be unambiguously inferred from
// the key format. Returns "" when the key is ambiguous or unrecognised.
//
// The ordering must agree with DetectCandidateKinds: "Project/42" is a
// valid Azure DevOps key AND a valid github/gitlab repo shape, so
// azuredevops has to be checked first, otherwise callers relying on
// DetectKind route Azure keys to GitHub.
func DetectKind(key string) string {
	if key == "" {
		return ""
	}
	if azureDevOpsRe.MatchString(key) {
		return "azuredevops"
	}
	if githubIssueRe.MatchString(key) || githubRepoRe.MatchString(key) {
		return "github"
	}
	return ""
}

// Issue is a provider-agnostic issue representation.
type Issue struct {
	Key         string    `json:"key"`
	Project     string    `json:"project"` // project key, e.g. "KAN"
	Type        string    `json:"type"`    // issue type, e.g. "Task", "Bug"
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	StatusType  Category  `json:"status_type,omitempty"` // semantic bucket, see Category
	Priority    string    `json:"priority"`
	Assignee    string    `json:"assignee"`
	Reporter    string    `json:"reporter"`
	Description string    `json:"description"`          // markdown
	URL         string    `json:"url,omitempty"`        // web URL for opening in browser
	UpdatedAt   time.Time `json:"updated_at"`           // last modification timestamp
	ParentKey   string    `json:"parent_key,omitempty"` // parent issue key (subtask support)
	Labels      []string  `json:"labels,omitempty"`     // tags/labels on the issue
}

// IsBug reports whether this issue represents a defect, normalised across
// trackers. A match is any segment equal to "bug" (case-insensitive) in Type
// or in any label, after splitting on '/' and ':'. This covers Shortcut's
// story_type="bug", Azure DevOps's WorkItemType="Bug", and label conventions
// like "bug", "Bug", "kind/bug", "type:bug" on Linear/GitHub/GitLab. Segment
// equality is used (not substring) so "debug" and "bugfix" do not match.
func (i Issue) IsBug() bool {
	if isBugToken(i.Type) {
		return true
	}
	for _, l := range i.Labels {
		if isBugToken(l) {
			return true
		}
	}
	return false
}

func isBugToken(s string) bool {
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == ':' }) {
		if strings.EqualFold(strings.TrimSpace(seg), "bug") {
			return true
		}
	}
	return false
}

// Comment is a provider-agnostic comment representation.
type Comment struct {
	ID      string
	Author  string
	Body    string // markdown
	Created time.Time
}

// ListOptions controls issue listing behaviour.
type ListOptions struct {
	Project      string
	MaxResults   int
	IncludeAll   bool      // when false, only open/active issues are returned
	UpdatedSince time.Time // when non-zero, only return issues updated after this time
}

// Read interfaces (implemented now).

// Lister lists issues for a project.
type Lister interface {
	ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error)
}

// Getter retrieves a single issue by key.
type Getter interface {
	GetIssue(ctx context.Context, key string) (*Issue, error)
}

// Provider combines all tracker operations into a single interface.
type Provider interface {
	Lister
	Getter
	Creator
	Commenter
	Deleter
	Transitioner
	Assigner
	CurrentUserGetter
	Editor
	StatusLister
}

// Instance represents a configured tracker backend ready for use.
type Instance struct {
	Name        string   // config entry name ("work", "personal"), empty for CLI-flag instances
	Kind        string   // "jira", "github", "linear"
	URL         string   // display URL
	User        string   // display user (Jira only)
	Description string   // optional human-readable description of what this tracker is for
	Role        string   // "pm", "engineering", or empty (inferred from kind)
	Safe        bool     // when true, destructive operations (deletes) are blocked
	Projects    []string // projects to index (e.g. ["KAN", "INFRA"])
	Provider    Provider
	// Forge is the optional code-host capability of this backend. It is set
	// only for backends that also host pull requests (e.g. GitHub); pure issue
	// trackers leave it nil. Kept separate from Provider so the tracker and
	// forge clients can be built and evolve independently.
	Forge forge.Forge
}

// InferRole returns the instance's role, falling back to kind-based inference
// when no explicit role is configured.
func (inst Instance) InferRole() string {
	if inst.Role != "" {
		return inst.Role
	}
	switch inst.Kind {
	case "shortcut":
		return "pm"
	case "linear":
		return "engineering"
	default:
		return ""
	}
}

// Write interfaces (future — not implemented yet).

// Creator creates new issues.
type Creator interface {
	CreateIssue(ctx context.Context, issue *Issue) (*Issue, error)
}

// Commenter manages issue comments.
type Commenter interface {
	ListComments(ctx context.Context, issueKey string) ([]Comment, error)
	AddComment(ctx context.Context, issueKey string, body string) (*Comment, error)
}

// Deleter deletes (or closes) an issue by key.
type Deleter interface {
	DeleteIssue(ctx context.Context, key string) error
}

// Transitioner moves an issue to a new status.
type Transitioner interface {
	TransitionIssue(ctx context.Context, key string, targetStatus string) error
}

// Assigner assigns an issue to a user.
type Assigner interface {
	AssignIssue(ctx context.Context, key string, userID string) error
}

// CurrentUserGetter retrieves the authenticated user's identifier.
type CurrentUserGetter interface {
	GetCurrentUser(ctx context.Context) (string, error)
}

// EditOptions specifies which fields to update on an issue.
// Nil pointer fields are left unchanged; non-nil fields are set (even if empty).
type EditOptions struct {
	Title       *string
	Description *string
}

// Editor updates an existing issue's title and/or description.
type Editor interface {
	EditIssue(ctx context.Context, key string, opts EditOptions) (*Issue, error)
}

// Category is the fixed, cross-tracker semantic bucket a Status belongs to.
//
// Every tracker exposes its own user-facing status names ("Ready for Review",
// "In QA", "Needs Design", …) that vary per team and per workflow. Category
// normalises those names into a small closed set the CLI can reason about
// uniformly — e.g. "is this issue done?", "is work in progress?", "what colour
// should this chip be in the TUI?".
//
// Two concepts, on purpose:
//
//   - Status.Name     = the label the user sees (tracker-specific, free-form).
//   - Status.Category = the semantic bucket (fixed enum, defined here).
//
// Per-tracker mapping lives in each provider client:
//
//	Linear        linearStateType()    — internal/linear/client.go
//	Azure DevOps  adoCategoryToType()  — internal/azuredevops/client.go
//	ClickUp       mapStatusType()      — internal/clickup/client.go
//	Shortcut      passthrough from API workflow state type
//	GitHub        open→Started, closed→Closed (binary)
//	GitLab        opened→Started, closed→Closed (binary)
//	Jira          not populated (transitions are dynamic per issue)
//
// Upstream trackers distinguish more buckets than we do (Linear has 5:
// Backlog, Unstarted, Started, Completed, Canceled; Azure DevOps has 5:
// Proposed, InProgress, Resolved, Completed, Removed). We collapse them to 4
// because that is the minimum the CLI needs to drive behaviour. To add a new
// category, extend this enum first, then update each client's mapping —
// do not sneak new values past the enum as bare strings.
type Category string

const (
	CategoryUnknown   Category = ""          // not populated (e.g. Jira transitions)
	CategoryUnstarted Category = "unstarted" // work not yet begun; Linear Backlog+Todo, ADO Proposed, ClickUp open
	CategoryStarted   Category = "started"   // actively in progress; Linear Started, ADO InProgress, GitHub/GitLab open
	CategoryDone      Category = "done"      // completed successfully; Linear Completed, ADO Resolved+Completed, ClickUp done+closed
	CategoryClosed    Category = "closed"    // finished but not completed (cancelled/removed); Linear Canceled, ADO Removed, GitHub/GitLab closed
)

// Status represents a workflow state an issue can be in.
//
// Name is what the user sees — tracker-specific, potentially team-specific,
// free-form. Category is the fixed semantic bucket (see Category doc).
type Status struct {
	Name     string   `json:"name"`
	Category Category `json:"type,omitempty"` // JSON key stays "type" for wire compatibility
}

// StatusLister lists available statuses for an issue.
// For Jira, only valid transitions from the current state are returned.
// For other trackers, all statuses for the project/workflow are returned.
type StatusLister interface {
	ListStatuses(ctx context.Context, key string) ([]Status, error)
}

// Resolve determines which tracker instance to use.
//
// When name is provided it finds the single instance whose Name matches.
// When name is empty it auto-detects: if keyHint allows inferring the tracker
// kind it filters to that kind; otherwise if all instances share one Kind it
// returns the first; if multiple kinds exist it returns an error.
func Resolve(name string, instances []Instance, keyHint string) (*Instance, error) {
	if name != "" {
		return resolveByName(name, instances)
	}
	return resolveAutoDetect(instances, keyHint)
}

// ResolveByKind returns the first instance matching the given tracker kind.
// When name is non-empty, it further filters to that named instance.
func ResolveByKind(kind string, instances []Instance, name string) (*Instance, error) {
	var filtered []Instance
	for _, inst := range instances {
		if inst.Kind == kind {
			filtered = append(filtered, inst)
		}
	}
	if len(filtered) == 0 {
		env := envHintForKind(kind)
		return nil, errors.WithDetails(
			fmt.Sprintf("no %s tracker found, set %s or add %ss: to .humanconfig", kind, env, kind),
			"kind", kind)
	}
	if name != "" {
		for i := range filtered {
			if filtered[i].Name == name {
				return &filtered[i], nil
			}
		}
		return nil, errors.WithDetails("tracker name not found for kind", "name", name, "kind", kind)
	}
	return &filtered[0], nil
}

// envHintForKind returns an example env var for the given tracker kind.
func envHintForKind(kind string) string {
	prefix := strings.ToUpper(kind)
	if kind == "azuredevops" {
		prefix = "AZURE"
	}
	suffix := "TOKEN"
	if kind == "jira" {
		suffix = "KEY"
	}
	return prefix + "_<NAME>_" + suffix
}

// resolveByName finds exactly one instance with the given name.
func resolveByName(name string, instances []Instance) (*Instance, error) {
	var matches []*Instance
	for i := range instances {
		if instances[i].Name == name {
			matches = append(matches, &instances[i])
		}
	}

	if len(matches) == 0 {
		return nil, errors.WithDetails("tracker name not found in .humanconfig", "name", name)
	}
	if len(matches) > 1 {
		return nil, errors.WithDetails("ambiguous tracker name found in multiple provider sections", "name", name)
	}
	return matches[0], nil
}

// resolveAutoDetect picks the sole kind of configured instances. When keyHint
// allows detecting a specific kind, instances are filtered to that kind first.
// If multiple kinds remain an error is returned asking the user to specify --tracker.
func resolveAutoDetect(instances []Instance, keyHint string) (*Instance, error) {
	if len(instances) == 0 {
		return nil, errors.WithDetails("no tracker configured, add jiras:, githubs:, gitlabs:, linears:, shortcuts:, or clickups: to .humanconfig.yaml")
	}

	// Narrow by key format. A key shape can be valid for several kinds
	// (e.g. "namespace/project#IID" is both GitHub and GitLab), so intersect
	// the shape's candidate kinds with what is actually configured rather than
	// committing to a single guessed kind — otherwise a gitlab-only config
	// rejects a perfectly resolvable GitLab key.
	if candidates := DetectCandidateKinds(keyHint); len(candidates) > 0 {
		candidateSet := make(map[string]bool, len(candidates))
		for _, k := range candidates {
			candidateSet[k] = true
		}
		var filtered []Instance
		filteredKinds := make(map[string]bool)
		for _, inst := range instances {
			if candidateSet[inst.Kind] {
				filtered = append(filtered, inst)
				filteredKinds[inst.Kind] = true
			}
		}
		if len(filtered) == 0 {
			return nil, errors.WithDetails("no tracker of detected kind configured",
				"candidateKinds", strings.Join(candidates, ","), "key", keyHint)
		}
		if len(filteredKinds) > 1 {
			return nil, errors.WithDetails("multiple tracker types match the key, specify --tracker=<name>",
				"key", keyHint)
		}
		return &filtered[0], nil
	}

	kinds := make(map[string]bool)
	for _, inst := range instances {
		kinds[inst.Kind] = true
	}

	if len(kinds) > 1 {
		return nil, errors.WithDetails("multiple tracker types configured, specify --tracker=<name>")
	}

	return &instances[0], nil
}
