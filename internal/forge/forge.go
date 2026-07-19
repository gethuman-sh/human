// Package forge abstracts code-hosting (forge) operations such as opening
// pull requests. It is deliberately separate from internal/tracker: a pull
// request is a code-repository concept, not an issue-tracker one. Some
// backends (GitHub, GitLab) are both a tracker and a forge and implement
// both interfaces; pure issue trackers (Jira, Linear, Shortcut, …) implement
// only tracker.Provider.
package forge

import (
	"context"
	"net/url"
	"strings"
)

// PullRequest carries both the request to open a pull request and the created
// result. Repo/Base/Head/Title/Body are inputs; Number/URL are populated on
// return (Title is echoed back from the forge).
type PullRequest struct {
	Repo  string // "owner/repo" (GitHub) or "group/project" (GitLab)
	Base  string // target branch the PR merges into (e.g. "main")
	Head  string // source branch holding the changes
	Title string
	Body  string

	Number int    // populated on return
	URL    string // populated on return
}

// Creator opens a pull request on a code-forge host.
type Creator interface {
	CreatePullRequest(ctx context.Context, pr *PullRequest) (*PullRequest, error)
}

// ChecksState summarizes the CI verdict on a pull request head. It collapses
// each provider's status/check-run vocabulary into the three states a deploy
// gate needs: still waiting, safe to merge, or must not merge.
type ChecksState string

const (
	ChecksPending ChecksState = "pending"
	ChecksPassing ChecksState = "passing"
	ChecksFailing ChecksState = "failing"
)

// ChecksReader reports the combined CI state of a pull request. A repository
// with no CI configured reports ChecksPassing — the deploy gate only blocks on
// evidence of failure or of checks still running, never on absence of CI.
type ChecksReader interface {
	PullRequestChecks(ctx context.Context, repo string, number int) (ChecksState, error)
}

// Merger merges a pull request into its base branch.
type Merger interface {
	MergePullRequest(ctx context.Context, repo string, number int) error
}

// BranchDeleter deletes a remote branch, used to clean up a pull request's
// source branch after merging.
type BranchDeleter interface {
	DeleteBranch(ctx context.Context, repo, branch string) error
}

// Forge aggregates the code-forge operations every provider must support. The
// deploy-oriented capabilities (ChecksReader, Merger, BranchDeleter) stay
// separate so a provider can be a Forge without the full deploy pipeline —
// callers type-assert for them.
type Forge interface {
	Creator
}

// IsForgeKind reports whether a tracker kind also acts as a code forge that
// can open pull requests. It gates which `human <kind>` command trees expose
// the `pr` subcommand, so pure issue trackers don't advertise an operation
// they can't perform.
func IsForgeKind(kind string) bool {
	switch kind {
	case "github":
		return true
	default:
		return false
	}
}

// KindForHost maps a git remote host to a forge kind, or "" if the host is not
// a recognised forge. It mirrors the host→kind mapping in tracker/urlparse.go
// so a repository's origin remote can be matched to a configured forge.
func KindForHost(host string) string {
	switch strings.ToLower(host) {
	case "github.com":
		return "github"
	default:
		return ""
	}
}

// ParseRemoteURL extracts the host and "owner/repo" path from a git remote URL,
// accepting the common forms:
//
//	https://github.com/owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	git@github.com:owner/repo.git   (scp-style, no scheme)
//
// A trailing ".git" and surrounding slashes are stripped. It returns ok=false
// for input it cannot parse into a non-empty host and repo path.
func ParseRemoteURL(raw string) (host, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}

	// scp-style syntax has no scheme: [user@]host:path.
	if !strings.Contains(raw, "://") {
		rest := raw
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		before, after, ok := strings.Cut(rest, ":")
		if !ok {
			return "", "", false
		}
		host = before
		repo = normalizeRepoPath(after)
		if host == "" || repo == "" {
			return "", "", false
		}
		return host, repo, true
	}

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	host = u.Hostname() // drops userinfo and port
	repo = normalizeRepoPath(u.Path)
	if host == "" || repo == "" {
		return "", "", false
	}
	return host, repo, true
}

// normalizeRepoPath trims slashes and a trailing ".git" from a remote path.
func normalizeRepoPath(p string) string {
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return strings.Trim(p, "/")
}
