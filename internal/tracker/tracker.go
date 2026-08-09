package tracker

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// githubIssueRe matches GitHub issue keys like "owner/repo#123".
var githubIssueRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#\d+$`)

// githubRepoRe matches GitHub project keys like "owner/repo".
var githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// jiraLinearIssueRe matches Jira/Linear issue keys like "KAN-42" or "ENG-123".
var jiraLinearIssueRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

// numericRe matches purely numeric keys like "123" (Shortcut).
var numericRe = regexp.MustCompile(`^\d+$`)

// shortcutDisplayRe matches Shortcut's own printed display form "SC-nnn"
// (case-insensitive) — the key the tool emits in commits, markers and PR
// titles. Its shape also matches jiraLinearIssueRe, so shortcut is offered as
// an extra candidate rather than replacing jira/linear; ambiguity is resolved
// by the FindTracker probe. The SC- prefix is Shortcut's fixed universal
// display prefix, so it is hardcoded rather than configured per workspace.
var shortcutDisplayRe = regexp.MustCompile(`^(?i:sc)-\d+$`)

// azureDevOpsRe matches Azure DevOps work item keys like "Project/42".
var azureDevOpsRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*/\d+$`)

// DetectCandidateKinds returns all tracker kinds whose key format matches the
// given key. The order is deterministic: azuredevops is checked before
// github/gitlab repo format since "Word/N" is a subset of "owner/repo".
//
// configuredKinds is an optional hint of which tracker kinds are actually
// configured in the workspace. With no hint, a bare numeric key stays a
// permissive shortcut candidate (pure format detection, used by callers that
// only compare shapes). When the caller supplies configuredKinds — the
// internal routing callers do — a bare numeric key is offered as a shortcut
// candidate only if a Shortcut tracker is actually configured, so a numeric
// key on a non-Shortcut workspace (e.g. ClickUp) is not force-routed to
// Shortcut (SC-2855).
func DetectCandidateKinds(key string, configuredKinds ...string) []string {
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

	if shortcutDisplayRe.MatchString(key) {
		// SC-nnn is Shortcut's explicit display form; always a shortcut candidate.
		kinds = append(kinds, "shortcut")
	} else if numericRe.MatchString(key) && numericOffersShortcut(configuredKinds) {
		kinds = append(kinds, "shortcut")
	}

	return kinds
}

// numericOffersShortcut reports whether a bare numeric key should be offered
// as a Shortcut candidate. With no configured-kind hint (the format-only
// callers) it stays permissive to preserve pure shape detection; when the
// caller supplies the configured kinds, a numeric key is a Shortcut candidate
// only if a Shortcut tracker is actually configured, so a numeric key on a
// non-Shortcut workspace is not force-routed to Shortcut (SC-2855).
func numericOffersShortcut(configuredKinds []string) bool {
	if len(configuredKinds) == 0 {
		return true
	}
	return slices.Contains(configuredKinds, "shortcut")
}

// instanceKinds returns the distinct kinds of the configured tracker
// instances, for gating format detection on what is actually configured.
func instanceKinds(instances []Instance) []string {
	kinds := make([]string, 0, len(instances))
	for _, inst := range instances {
		kinds = append(kinds, inst.Kind)
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
	// SC-nnn carries no project prefix (the "SC" is a display marker, not a
	// project). Check before jiraLinearIssueRe, whose shape it shares, so it is
	// not mistaken for a "SC" project.
	if shortcutDisplayRe.MatchString(key) {
		return ""
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

// shortcutCommitPrefix is Shortcut's fixed universal display prefix. A bare
// numeric story ID is not attributable in a commit reference on its own, so it
// is normalized to this prefixed form — the same key Shortcut prints and that
// `human commits for` and the commit-msg hook expect. It is hardcoded (not
// configured per workspace) for the same reason shortcutDisplayRe is.
const shortcutCommitPrefix = "SC-"

// trimKeyBrackets strips surrounding whitespace and one layer of [ ] from a
// key, the form the board/pipeline sometimes passes internally.
func trimKeyBrackets(key string) string {
	trimmed := strings.TrimSpace(key)
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
}

// CanonicalCommitKey normalizes a ticket key to the canonical form used in
// commit references. kind is the owning tracker's kind. A bare numeric key is
// a Shortcut story ID ONLY on a Shortcut tracker, so the "SC-" display prefix
// is applied only when kind == "shortcut"; a numeric key on any other tracker
// (or when the owning tracker is unknown, kind == "") is returned unchanged
// rather than guessed into Shortcut's prefix, which silently broke
// key->commit lookups on numeric-keyed non-Shortcut trackers like ClickUp
// (SC-2855). Every other key format already carries its own prefix and is
// returned unchanged.
func CanonicalCommitKey(key, kind string) string {
	trimmed := trimKeyBrackets(key)
	if kind == "shortcut" && numericRe.MatchString(trimmed) {
		return shortcutCommitPrefix + trimmed
	}
	return trimmed
}

// CommitKind resolves the owning tracker kind used to canonicalize key into
// its commit reference, from the configured instances alone (no network
// probe, so it stays cheap and offline-safe on the commit path). Only a bare
// numeric key is ambiguous: it is attributed to Shortcut when a Shortcut
// tracker is configured, otherwise the kind is left unknown ("") so a
// numeric key from another tracker (e.g. ClickUp) is never guessed into
// Shortcut's "SC-" prefix (SC-2855). Any key that already carries its own
// format prefix is irrelevant to canonicalization, so "" is returned.
func CommitKind(key string, instances []Instance) string {
	if !numericRe.MatchString(trimKeyBrackets(key)) {
		return ""
	}
	for _, inst := range instances {
		if inst.Kind == "shortcut" {
			return "shortcut"
		}
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
	candidates := DetectCandidateKinds(key, instanceKinds(instances)...)
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
// DetectKind route Azure keys to GitHub. "SC-nnn" is Shortcut's own
// unambiguous display form and resolves to "shortcut".
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
	if shortcutDisplayRe.MatchString(key) {
		return "shortcut"
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
	// Links are this issue's relationships to others. Empty on backends that
	// cannot report them, which is a correct answer rather than a gap.
	Links []IssueLink `json:"links,omitempty"`
}

// LinkKind names a relationship between two issues.
//
// The distinction is load-bearing rather than cosmetic: only a directional link
// can express that one piece of work must finish before another starts, and
// only that can gate anything. A symmetric "related" records an association and
// implies no order.
type LinkKind string

const (
	// LinkRelated is a symmetric association: these two issues concern each
	// other, in no particular order.
	LinkRelated LinkKind = "related"
	// LinkBlocks is directional: the subject must be finished before the object
	// can proceed.
	LinkBlocks LinkKind = "blocks"
)

// IssueLink is one relationship, seen from the issue that carries it.
//
// Direction is resolved here, at the provider boundary, and never left for
// consumers to derive from raw subject/object identifiers. Every place that
// re-derives it is a chance to get it backwards, and a reversed blocker gates
// the wrong ticket — stalling work that was ready while releasing work that was
// not.
type IssueLink struct {
	// Key identifies the issue at the other end.
	Key string `json:"key"`
	// Kind is the relationship. For LinkRelated, Inbound carries no meaning.
	Kind LinkKind `json:"kind"`
	// Inbound reports the direction for a blocking link: true means the OTHER
	// issue blocks this one, so this issue is the one that must wait.
	Inbound bool `json:"inbound,omitempty"`
}

// BlockedBy returns the keys of issues that must finish before this one can
// proceed. It reads the links; it does not know whether those issues are still
// open, which is the caller's to establish.
func (i Issue) BlockedBy() []string {
	var keys []string
	for _, l := range i.Links {
		if l.Kind == LinkBlocks && l.Inbound {
			keys = append(keys, l.Key)
		}
	}
	return keys
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
	return slices.ContainsFunc(i.Labels, isBugToken)
}

func isBugToken(s string) bool {
	return hasToken(s, "bug")
}

// BugLabel is the label that marks a defect on providers without a native bug
// issue type, matching the classification IsBug already accepts.
const BugLabel = "bug"

// IsBugType reports whether the type string names the defect issue type,
// normalised like IsBug ("Bug", "bug", "type:bug" all match). Providers use
// it to map a bug-typed Issue onto their native defect marker at create time.
func IsBugType(s string) bool {
	return isBugToken(s)
}

// CreateLabels returns the labels a provider WITHOUT a native bug or security
// type must send when creating i: i.Labels, plus BugLabel or SecurityLabel when
// i is bug- or security-typed and not already so-labeled. Without this
// translation a typed Issue would lose its kind marking on label-only trackers
// (Linear, GitHub, GitLab, ClickUp) and IsBug/IsSecurity could never recognise
// the created ticket. Providers with a native type (Jira, Shortcut, Azure
// DevOps) map i.Type directly instead. Type is a single string, so at most one
// of the two branches ever fires.
func CreateLabels(i *Issue) []string {
	labels := i.Labels
	if isBugToken(i.Type) && !slices.ContainsFunc(labels, isBugToken) {
		labels = append(append([]string{}, labels...), BugLabel)
	}
	if isSecurityToken(i.Type) && !slices.ContainsFunc(labels, isSecurityToken) {
		labels = append(append([]string{}, labels...), SecurityLabel)
	}
	return labels
}

// RetypeLabels returns the label edits that change a ticket's KIND on a
// provider without a native issue type, where the kind is carried by a label
// and nothing else: add is the label the new kind needs, remove is every label
// currently marking a kind the ticket is no longer.
//
// It is CreateLabels' counterpart for an existing ticket, and it exists for the
// same reason: without the translation, retyping on a label-only tracker
// (Linear, GitHub, GitLab, ClickUp) would silently do nothing.
//
// current is the ticket's live label set, which the caller must read first. It
// is not optional: kinds are recognised by token ("bug", "kind/bug",
// "type:bug" all count), so removing only the canonical spelling would leave a
// ticket labelled "kind/bug" still reading as a bug after a retype that
// reported success — the one outcome a retype must never produce.
func RetypeLabels(current []string, newType string) (add, remove []string) {
	wantBug, wantSecurity := isBugToken(newType), isSecurityToken(newType)
	for _, l := range current {
		switch {
		case isBugToken(l) && !wantBug, isSecurityToken(l) && !wantSecurity:
			remove = append(remove, l)
		}
	}
	if wantBug && !slices.ContainsFunc(current, isBugToken) {
		add = append(add, BugLabel)
	}
	if wantSecurity && !slices.ContainsFunc(current, isSecurityToken) {
		add = append(add, SecurityLabel)
	}
	return add, remove
}

// RetypeIntoLabels turns a requested kind change into ordinary label edits, for
// a provider whose kind IS a label (Linear, GitHub, GitLab, ClickUp). It reads
// the ticket's live labels through g — the retype cannot be computed without
// them — and hands back opts with the work folded into AddLabels/RemoveLabels
// and Type cleared, so the provider's existing label-merge path carries it and
// cannot forget it halfway.
//
// Returns opts untouched when no retype was asked for, so every provider can
// call it unconditionally at the top of EditIssue and pay one extra read only
// when a kind actually changes.
func RetypeIntoLabels(ctx context.Context, g Getter, key string, opts EditOptions) (EditOptions, error) {
	if opts.Type == nil {
		return opts, nil
	}
	issue, err := g.GetIssue(ctx, key)
	if err != nil {
		return opts, errors.WrapWithDetails(err, "reading current labels to retype the issue", "key", key)
	}
	return applyRetypeLabels(opts, issue.Labels), nil
}

// applyRetypeLabels is RetypeIntoLabels' pure half: the fold itself, given
// labels already read.
func applyRetypeLabels(opts EditOptions, current []string) EditOptions {
	add, remove := RetypeLabels(current, *opts.Type)
	opts.AddLabels = append(slices.Clone(opts.AddLabels), add...)
	opts.RemoveLabels = append(slices.Clone(opts.RemoveLabels), remove...)
	opts.Type = nil
	return opts
}

// SecurityLabel marks a security/vulnerability ticket on providers without a
// native security type, matching the classification IsSecurity accepts.
const SecurityLabel = "security"

// IsSecurity reports whether this issue represents a security vulnerability,
// normalised across trackers exactly like IsBug: any segment equal to
// "security" (case-insensitive) in Type or in any label, after splitting on
// '/' and ':'. Covers "security", "Security", "kind/security", "type:security";
// segment equality keeps words like "insecurity" from matching. A security
// ticket is its own kind, distinct from a bug — the two tokens never overlap.
func (i Issue) IsSecurity() bool {
	if isSecurityToken(i.Type) {
		return true
	}
	return slices.ContainsFunc(i.Labels, isSecurityToken)
}

func isSecurityToken(s string) bool {
	return hasToken(s, "security")
}

// IsSecurityType reports whether the type string names the security issue type,
// normalised like IsSecurity. Providers use it to map a security-typed Issue
// onto their native marker (or the SecurityLabel) at create time.
func IsSecurityType(s string) bool {
	return isSecurityToken(s)
}

// IdeaLabel is the canonical label marking a ticket as a raw idea — the first of
// the three forms an evolving ticket takes (idea, pm, planned). "Stage" is not
// the word for it: that is what the board is running. It is namespaced so it
// cannot collide with a team's existing labels; classification additionally
// accepts the bare token (see IsIdea) so existing "idea" conventions count.
const IdeaLabel = "human/idea"

// IsIdea reports whether this issue is a raw idea, normalised across trackers
// exactly like IsBug: any segment equal to "idea" in Type or in any label,
// after splitting on '/' and ':'. Covers "idea", "human/idea", "kind/idea",
// "type:idea"; segment equality keeps "ideation" from matching.
func (i Issue) IsIdea() bool {
	if hasToken(i.Type, "idea") {
		return true
	}
	for _, l := range i.Labels {
		if hasToken(l, "idea") {
			return true
		}
	}
	return false
}

// hasToken reports whether any '/'- or ':'-separated segment of s equals
// token case-insensitively — the shared normalisation for cross-tracker
// label/type classification.
func hasToken(s, token string) bool {
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == ':' }) {
		if strings.EqualFold(strings.TrimSpace(seg), token) {
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
	// Unattended says this listing is a poll loop rather than a person waiting
	// for an answer: the board's refresh, the reconcile sweep, the record sync.
	//
	// It exists because "everything I can see" costs wildly different things on
	// different backends. An unscoped Shortcut listing is one cheap call; an
	// unscoped GitHub listing is a search across every issue the token can see,
	// on an endpoint with its own quota, which a loop exhausts in minutes
	// ([SC-3888]). The caller knows whether it is a loop; only the backend knows
	// what the request costs. So the caller declares the former and the backend
	// decides the latter, rather than either guessing about the other.
	//
	// A hand-run command leaves this false: someone who typed `human list` is
	// waiting for the answer and may have whatever they asked for.
	Unattended bool
}

// Read interfaces (implemented now).

// Lister lists issues for a project.
type Lister interface {
	ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error)
}

// IssuePage is a page of issues plus the completeness signal a caller needs to
// act safely on an absence. Truncated is true when the backend applied
// ListOptions.MaxResults and more issues existed than were returned, so the
// caller knows the fetch is partial and must not treat a missing key as gone —
// the board's prune guard depends on this to avoid erasing saved view state for
// tickets that merely fell past the cap (SC-1693).
type IssuePage struct {
	Issues    []Issue
	Truncated bool
}

// PagedLister is an optional Lister capability: a backend that enforces
// ListOptions.MaxResults by returning a bounded page (rather than paging to
// completion) implements it to report whether the page was cut short. Backends
// that always return the complete set (they page internally, or ignore the cap)
// need not implement it — ListIssuesPage treats their result as never
// truncated. Kept separate from Lister so existing implementations and their
// callers are undisturbed.
type PagedLister interface {
	ListIssuesPage(ctx context.Context, opts ListOptions) (IssuePage, error)
}

// ListIssuesPage fetches a page of issues and reports truncation, preferring a
// Lister's optional PagedLister capability. A backend that pages to completion
// exposes only ListIssues; its result is complete, so it is reported as never
// truncated. This lets truncation-sensitive callers (board pruning) treat every
// backend uniformly without a type switch at each call site.
func ListIssuesPage(ctx context.Context, l Lister, opts ListOptions) (IssuePage, error) {
	if pl, ok := l.(PagedLister); ok {
		return pl.ListIssuesPage(ctx, opts)
	}
	issues, err := l.ListIssues(ctx, opts)
	return IssuePage{Issues: issues}, err
}

// CollectPaged fills a bounded fetch up to maxResults for a backend whose
// per-request page size is smaller than the board's cap, so the explicit cap
// behaves the same on every backend rather than degrading to the backend's page
// size (SC-1693). fetch returns one page of issues (pageIndex counts from 0) and
// whether the backend signals a further page; a cursor-based backend keeps its
// own cursor in the closure and ignores pageIndex. Collection stops as soon as
// the cap is reached or the backend reports no further page. Truncated is true
// only when issues genuinely remain beyond the cap — either the backend still
// signals a next page at the cap, or a page pushed the total past it — so a
// fetch that ends exactly on the last page is reported complete and the board's
// prune guard does not fire on a full backlog. maxResults <= 0 means "no cap":
// a single page is fetched and Truncated mirrors the backend's next-page signal,
// matching the single-query backends (Linear).
func CollectPaged(maxResults int, fetch func(pageIndex int) (issues []Issue, hasNext bool, err error)) (IssuePage, error) {
	var collected []Issue
	for pageIndex := 0; ; pageIndex++ {
		issues, hasNext, err := fetch(pageIndex)
		if err != nil {
			return IssuePage{}, err
		}
		collected = append(collected, issues...)

		if maxResults <= 0 {
			return IssuePage{Issues: collected, Truncated: hasNext}, nil
		}
		if len(collected) >= maxResults {
			truncated := hasNext || len(collected) > maxResults
			if len(collected) > maxResults {
				collected = collected[:maxResults]
			}
			return IssuePage{Issues: collected, Truncated: truncated}, nil
		}
		if !hasNext {
			return IssuePage{Issues: collected, Truncated: false}, nil
		}
	}
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
	Linker
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
	Projects    []string // projects to index (board scope; empty = show all)
	// CreateIn is where NEW tickets are filed, independent of Projects (the
	// board scope). Empty means "default to Projects[0]" so setups that predate
	// the split keep filing exactly where they did. Splitting the two lets a
	// board show all work (Projects empty) while still filing into one place
	// (CreateIn set) — SC-1959.
	CreateIn string
	Provider Provider
}

// FilingTarget returns where new tickets should be filed for this instance.
// An explicit CreateIn always wins; otherwise it falls back to the first
// indexed project so existing single-knob configs are unchanged. Empty means
// no filing target is configured — callers on the write path must reject this
// rather than file group-less and land the ticket off every board (SC-1959).
func (inst Instance) FilingTarget() string {
	if inst.CreateIn != "" {
		return inst.CreateIn
	}
	if len(inst.Projects) > 0 {
		return inst.Projects[0]
	}
	return ""
}

// InferRole returns the instance's role. An explicit role always wins. With no
// explicit role, only the pm role is inferred (for Shortcut boards) so read-side
// board scanning keeps working out of the box; the engineering role is never
// inferred. Engineering (split) topology is opt-in: it turns on only when a
// tracker carries an explicit role: engineering in .humanconfig. Inferring
// engineering from the Linear kind silently flipped single-tracker setups back
// into split topology and minted unwanted engineering tickets ([SC-254]).
func (inst Instance) InferRole() string {
	if inst.Role != "" {
		return inst.Role
	}
	if inst.Kind == "shortcut" {
		return "pm"
	}
	return ""
}

// ValidateTopology reports a divergence between the topology a config DECLARES
// and the topology its RESOLVABLE credentials can actually run. declared is the
// set of tracker statuses from DiagnoseTrackers (config view); resolvedEngineering
// reports whether any instance whose role resolves to "engineering" actually
// loaded (credentials present).
//
// Returns a non-nil error when any tracker declares role: engineering in config
// but did not resolve — that is the exact silent split->single fallback SC-660
// rule 7 forbids: the same config would run split topology on a machine holding
// the token and single-tracker on one that does not.
func ValidateTopology(declared []TrackerStatus, resolvedEngineering bool) error {
	declaresEngineering := false
	for _, s := range declared {
		if s.Role == "engineering" {
			declaresEngineering = true
			break
		}
	}
	if declaresEngineering && !resolvedEngineering {
		return errors.WithDetails(
			"config declares an engineering-role tracker but its credentials did not resolve; "+
				"this would silently run single-tracker topology here and split topology elsewhere — "+
				"fix the engineering tracker's token or remove its role: engineering declaration",
			"declared", "engineering", "resolved", "false")
	}
	return nil
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

// Linker relates two issues in the same tracker. The relation is the generic,
// symmetric "relates to" — richer typed relations (blocks, duplicates) are
// deliberately out of scope so every backend can honour the same call.
// Backends without a native relation API (GitHub) record the relation as a
// cross-reference comment on the first issue, which is that ecosystem's
// convention for relating issues.
type Linker interface {
	// LinkIssues relates key to otherKey. For LinkBlocks the direction is
	// subject-verb-object: key blocks otherKey.
	//
	// A backend that cannot express the requested kind must return an error
	// naming that limitation rather than writing a weaker relation. A "blocks"
	// link silently stored as "related" would gate nothing while appearing to,
	// which is the failure a caller cannot detect.
	LinkIssues(ctx context.Context, key string, otherKey string, kind LinkKind) error
	// UnlinkIssues removes the relationship between two issues. Removing a
	// dependency is how work held behind a mistaken or abandoned blocker is
	// released, so it is a mutation in its own right.
	UnlinkIssues(ctx context.Context, key string, otherKey string) error
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

// ReporterAssigner makes an issue's own reporter its owner.
//
// It is a provider capability rather than a caller-side compose of "read the
// reporter, then assign them" because that compose is impossible from outside:
// Issue.Reporter is a display NAME while AssignIssue takes a user ID, and
// nothing maps one to the other. A backend holds both on the issue it just
// fetched, so only the backend can join them.
type ReporterAssigner interface {
	AssignToReporter(ctx context.Context, key string) error
}

// CurrentUserNamer resolves the authenticated user's DISPLAY NAME — the same
// string space as Issue.Assignee and Issue.Reporter, so the board can decide
// "is this card mine?" by string equality against the owner. This is distinct
// from CurrentUserGetter, which returns the opaque member ID used to assign a
// ticket to self: an ID never equals a display name, so owner-vs-me needs this.
// Optional: a provider that cannot cheaply resolve its own display name simply
// does not implement it, and the board dims nothing for that tracker.
type CurrentUserNamer interface {
	CurrentUserName(ctx context.Context) (string, error)
}

// EditOptions specifies which fields to update on an issue.
// Nil pointer fields are left unchanged; non-nil fields are set (even if empty).
type EditOptions struct {
	Title       *string
	Description *string
	// Type retypes the issue: "Bug" and "Security" name the two kinds the
	// pipeline routes on, anything else is ordinary product work. nil leaves
	// the kind alone, so an edit that does not mention it can never change it.
	//
	// A kind is chosen at create time and, until this existed, could never be
	// corrected — the only remedies were the tracker's own web UI or refiling
	// the ticket and losing its key, history and comments (SC-3051). Every
	// provider expresses it in its own vocabulary: a native type field where
	// one exists, a label swap where the kind is a label (RetypeLabels).
	Type *string
	// AddLabels are labels to add to the issue. Providers whose label model
	// requires pre-existing label entities create them on the fly.
	AddLabels []string
	// RemoveLabels are labels to remove; labels the issue does not carry are
	// ignored rather than treated as an error, so a label swap is idempotent.
	RemoveLabels []string
}

// Editor updates an existing issue's title, description, kind, and/or labels.
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

// ResolveByKind returns the first tracker instance matching the given kind.
// When name is non-empty, it further filters to that named instance. Forges of
// the same kind are not here to be skipped: they are a separate domain with a
// separate list and their own forge.Resolve ([SC-3876]).
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

// resolveByName finds exactly one tracker instance with the given name. A forge
// may share the name without colliding: --tracker=<name> addresses a tracker,
// and forges are not in this list at all ([SC-3876]).
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
	if candidates := DetectCandidateKinds(keyHint, instanceKinds(instances)...); len(candidates) > 0 {
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
