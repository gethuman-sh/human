package tracker

import (
	"net/url"
	"slices"
	"strings"
)

// ParsedURL holds the results of parsing a tracker URL.
type ParsedURL struct {
	Kind    string // "jira", "github", "gitlab", "linear", "azuredevops", "shortcut"
	BaseURL string // API-compatible base URL (e.g., "https://amazingcto.atlassian.net")
	Key     string // issue key in CLI format (e.g., "HUM-4", "owner/repo#42")
	Org     string // Azure DevOps org or Shortcut org slug
}

// IsURL returns true if the input looks like a URL rather than an issue key.
func IsURL(input string) bool {
	return strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://")
}

// ParseURL extracts tracker kind, base URL, and issue key from a tracker URL.
// Returns (nil, false) if the URL doesn't match any known tracker pattern.
func ParseURL(rawURL string) (*ParsedURL, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, false
	}

	host := strings.ToLower(u.Host)
	segments := splitPath(u.Path)

	// Try each parser in order.
	parsers := []func(string, *url.URL, []string) (*ParsedURL, bool){
		parseJiraURL,
		parseGitHubURL,
		parseGitLabURL,
		parseLinearURL,
		parseAzureDevOpsURL,
		parseShortcutURL,
	}

	for _, p := range parsers {
		if result, ok := p(host, u, segments); ok {
			return result, true
		}
	}

	return nil, false
}

// splitPath splits a URL path into non-empty segments.
func splitPath(path string) []string {
	var segments []string
	for s := range strings.SplitSeq(path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	return segments
}

// parseJiraURL handles:
//   - https://{org}.atlassian.net/browse/{KEY}
//   - https://{org}.atlassian.net/jira/...?selectedIssue={KEY}
//   - https://{host}/browse/{KEY} (self-hosted, validated by key regex)
func parseJiraURL(host string, u *url.URL, segments []string) (*ParsedURL, bool) {
	baseURL := u.Scheme + "://" + u.Host

	// Check selectedIssue query param (board URLs).
	if key := u.Query().Get("selectedIssue"); key != "" && jiraLinearIssueRe.MatchString(key) {
		if strings.HasSuffix(host, ".atlassian.net") || containsSegment(segments, "jira") {
			return &ParsedURL{Kind: "jira", BaseURL: baseURL, Key: key}, true
		}
	}

	// Check /browse/{KEY} path.
	for i, seg := range segments {
		if seg == "browse" && i+1 < len(segments) {
			key := segments[i+1]
			if jiraLinearIssueRe.MatchString(key) {
				return &ParsedURL{Kind: "jira", BaseURL: baseURL, Key: key}, true
			}
		}
	}

	// Atlassian Cloud with selectedIssue but key didn't match jira regex —
	// still try it if host is atlassian.net.
	if strings.HasSuffix(host, ".atlassian.net") {
		if key := u.Query().Get("selectedIssue"); key != "" {
			return &ParsedURL{Kind: "jira", BaseURL: baseURL, Key: key}, true
		}
	}

	return nil, false
}

// parseGitHubURL handles:
//   - https://github.com/{owner}/{repo}/issues/{num}
//   - https://github.com/{owner}/{repo}/pull/{num}
func parseGitHubURL(host string, u *url.URL, segments []string) (*ParsedURL, bool) {
	if host != "github.com" {
		return nil, false
	}

	// Need at least: owner, repo, issues|pull, num
	if len(segments) < 4 {
		return nil, false
	}

	owner := segments[0]
	repo := segments[1]
	action := segments[2]
	num := segments[3]

	if (action == "issues" || action == "pull") && numericRe.MatchString(num) {
		key := owner + "/" + repo + "#" + num
		return &ParsedURL{Kind: "github", BaseURL: "https://api.github.com", Key: key}, true
	}

	return nil, false
}

// parseGitLabURL handles:
//   - https://gitlab.com/{group}/{project}/-/issues/{num}
func parseGitLabURL(host string, u *url.URL, segments []string) (*ParsedURL, bool) {
	if host != "gitlab.com" {
		return nil, false
	}

	// Find the "/-/issues/{num}" pattern; group/project may be nested.
	issuesIdx := -1
	for i, seg := range segments {
		if seg == "-" && i+2 < len(segments) && segments[i+1] == "issues" && numericRe.MatchString(segments[i+2]) {
			issuesIdx = i
			break
		}
	}
	if issuesIdx < 1 {
		return nil, false
	}

	project := strings.Join(segments[:issuesIdx], "/")
	num := segments[issuesIdx+2]
	key := project + "#" + num

	return &ParsedURL{Kind: "gitlab", BaseURL: "https://gitlab.com", Key: key}, true
}

// parseLinearURL handles:
//   - https://linear.app/{team}/issue/{KEY}/...
func parseLinearURL(host string, _ *url.URL, segments []string) (*ParsedURL, bool) {
	if host != "linear.app" {
		return nil, false
	}

	// Pattern: {team}, "issue", {KEY}, optional slug
	if len(segments) < 3 {
		return nil, false
	}

	if segments[1] != "issue" {
		return nil, false
	}

	key := segments[2]
	if !jiraLinearIssueRe.MatchString(key) {
		return nil, false
	}

	return &ParsedURL{Kind: "linear", BaseURL: "https://api.linear.app", Key: key}, true
}

// parseAzureDevOpsURL handles:
//   - https://dev.azure.com/{org}/{project}/_workitems/edit/{num}
func parseAzureDevOpsURL(host string, _ *url.URL, segments []string) (*ParsedURL, bool) {
	if host != "dev.azure.com" {
		return nil, false
	}

	// Pattern: {org}, {project}, "_workitems", "edit", {num}
	if len(segments) < 5 {
		return nil, false
	}

	if segments[2] != "_workitems" || segments[3] != "edit" {
		return nil, false
	}

	org := segments[0]
	project := segments[1]
	num := segments[4]

	if !numericRe.MatchString(num) {
		return nil, false
	}

	key := project + "/" + num
	return &ParsedURL{Kind: "azuredevops", BaseURL: "https://dev.azure.com", Key: key, Org: org}, true
}

// parseShortcutURL handles:
//   - https://app.shortcut.com/{org}/story/{num}/...
func parseShortcutURL(host string, _ *url.URL, segments []string) (*ParsedURL, bool) {
	if host != "app.shortcut.com" {
		return nil, false
	}

	// Pattern: {org}, "story", {num}, optional slug
	if len(segments) < 3 {
		return nil, false
	}

	if segments[1] != "story" {
		return nil, false
	}

	num := segments[2]
	if !numericRe.MatchString(num) {
		return nil, false
	}

	return &ParsedURL{Kind: "shortcut", BaseURL: "https://api.app.shortcut.com", Key: num}, true
}

// containsSegment checks if a path segment exists in the list.
func containsSegment(segments []string, target string) bool {
	return slices.Contains(segments, target)
}
