package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
	"github.com/gethuman-sh/human/internal/forge"
)

var (
	_ forge.Forge             = (*Client)(nil)
	_ forge.ChecksReader      = (*Client)(nil)
	_ forge.Merger            = (*Client)(nil)
	_ forge.BranchDeleter     = (*Client)(nil)
	_ forge.PullRequestFinder = (*Client)(nil)
)

// Client is a GitHub REST API client scoped to code-forge (pull request)
// operations. It is deliberately separate from the issue-tracker client so the
// forge and tracker capabilities can be wired and evolve independently, even
// though both talk to the same GitHub API.
type Client struct {
	api *apiclient.Client
}

// New creates a GitHub forge client with the given base URL and token.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BearerToken(token)),
			apiclient.WithHeader("Accept", "application/vnd.github+json"),
			apiclient.WithProviderName("github"),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// CreatePullRequest implements forge.Creator via the GitHub pulls API.
func (c *Client) CreatePullRequest(ctx context.Context, pr *forge.PullRequest) (*forge.PullRequest, error) {
	owner, repo, err := splitProject(pr.Repo)
	if err != nil {
		return nil, err
	}

	payload := pullCreateRequest{
		Title: pr.Title,
		Head:  pr.Head,
		Base:  pr.Base,
		Body:  pr.Body,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling pull request request",
			"repo", pr.Repo)
	}

	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repo))
	resp, err := c.api.Do(ctx, http.MethodPost, path, "", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var result pullCreateResponse
	if err := apiclient.DecodeJSON(resp, &result, "repo", pr.Repo); err != nil {
		return nil, err
	}

	return &forge.PullRequest{
		Repo:   pr.Repo,
		Base:   pr.Base,
		Head:   pr.Head,
		Title:  result.Title,
		Body:   pr.Body,
		Number: result.Number,
		URL:    result.HTMLURL,
	}, nil
}

// FindOpenPullRequest implements forge.PullRequestFinder. GitHub's list-pulls
// endpoint filters by head as "owner:branch"; with state=open it returns the
// live PR for the branch (at most one), which a deploy retry adopts instead of
// re-creating (SC-989). No match returns (nil, nil).
func (c *Client) FindOpenPullRequest(ctx context.Context, repoName, head string) (*forge.PullRequest, error) {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repo))
	query := url.Values{}
	// GitHub filters list-pulls by head as "owner:branch".
	query.Set("head", owner+":"+head)
	query.Set("state", "open")
	resp, err := c.api.Do(ctx, http.MethodGet, path, query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var list []pullListItem
	if err := apiclient.DecodeJSON(resp, &list, "repo", repoName, "head", head); err != nil {
		return nil, err
	}
	for _, item := range list {
		if item.State == "open" && item.Head.Ref == head {
			return &forge.PullRequest{
				Repo:   repoName,
				Head:   head,
				Title:  item.Title,
				Number: item.Number,
				URL:    item.HTMLURL,
			}, nil
		}
	}
	return nil, nil
}

// PullRequestChecks implements forge.ChecksReader. GitHub reports CI through
// two parallel systems — check runs (GitHub Actions and modern apps) and
// commit statuses (legacy integrations) — so both are consulted: any failure
// in either fails the whole verdict, anything still running keeps it pending,
// and a repository reporting through neither passes (no CI configured is not a
// blocker).
func (c *Client) PullRequestChecks(ctx context.Context, repoName string, number int) (forge.ChecksState, error) {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return "", err
	}

	sha, err := c.pullHeadSHA(ctx, owner, repo, number)
	if err != nil {
		return "", err
	}

	runsPath := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	resp, err := c.api.Do(ctx, http.MethodGet, runsPath, "", nil)
	if err != nil {
		return "", err
	}
	var runs checkRunsResponse
	if err := apiclient.DecodeJSON(resp, &runs, "repo", repoName); err != nil {
		return "", err
	}

	statusPath := fmt.Sprintf("/repos/%s/%s/commits/%s/status",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	resp, err = c.api.Do(ctx, http.MethodGet, statusPath, "", nil)
	if err != nil {
		return "", err
	}
	var combined combinedStatusResponse
	if err := apiclient.DecodeJSON(resp, &combined, "repo", repoName); err != nil {
		return "", err
	}

	return combineChecks(runs, combined), nil
}

// combineChecks folds check runs and the combined commit status into one
// verdict. Failure anywhere wins over pending, pending wins over passing.
func combineChecks(runs checkRunsResponse, combined combinedStatusResponse) forge.ChecksState {
	state := forge.ChecksPassing
	for _, run := range runs.CheckRuns {
		switch run.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required":
			return forge.ChecksFailing
		}
		if run.Status != "completed" {
			state = forge.ChecksPending
		}
	}
	switch combined.State {
	case "failure", "error":
		return forge.ChecksFailing
	case "pending":
		// GitHub reports state "pending" with zero statuses when only check
		// runs exist — that carries no signal, so it must not hold the gate.
		if combined.TotalCount > 0 {
			state = forge.ChecksPending
		}
	}
	return state
}

// pullHeadSHA fetches the head commit of a pull request, the ref both CI
// reporting systems key their results on.
func (c *Client) pullHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.api.Do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return "", err
	}
	var pull pullGetResponse
	if err := apiclient.DecodeJSON(resp, &pull, "number", number); err != nil {
		return "", err
	}
	if pull.Head.SHA == "" {
		return "", errors.WithDetails("pull request has no head SHA", "number", number)
	}
	return pull.Head.SHA, nil
}

// PullRequestMergeable implements forge.MergeReader. GitHub computes the
// mergeable flag asynchronously and returns null until it is ready; a null
// value is reported as not mergeable so a caller never merges on an unknown
// state.
func (c *Client) PullRequestMergeable(ctx context.Context, repoName string, number int) (bool, error) {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return false, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.api.Do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return false, err
	}
	var pull pullGetResponse
	if err := apiclient.DecodeJSON(resp, &pull, "number", number); err != nil {
		return false, err
	}
	return pull.Mergeable != nil && *pull.Mergeable, nil
}

// MergePullRequest implements forge.Merger with a merge commit, preserving the
// branch's individual commits (and their ticket references) on the mainline.
func (c *Client) MergePullRequest(ctx context.Context, repoName string, number int) error {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return err
	}
	body, err := json.Marshal(mergeRequest{MergeMethod: "merge"})
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling merge request", "repo", repoName)
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.api.Do(ctx, http.MethodPut, path, "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	var result mergeResponse
	if err := apiclient.DecodeJSON(resp, &result, "repo", repoName); err != nil {
		return err
	}
	if !result.Merged {
		// The forge's reason goes into the message itself: the deploy pipeline
		// surfaces err.Error() on the board card, where details are invisible.
		return errors.WithDetails("pull request was not merged: "+result.Message, "repo", repoName, "number", number)
	}
	return nil
}

// DeleteBranch implements forge.BranchDeleter via the git refs API.
func (c *Client) DeleteBranch(ctx context.Context, repoName, branch string) error {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(branch))
	resp, err := c.api.Do(ctx, http.MethodDelete, path, "", nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// splitProject parses an "owner/repo" string. Duplicated from the tracker-side
// GitHub client so the forge package stands alone without importing it.
func splitProject(project string) (string, string, error) {
	parts := strings.SplitN(project, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.WithDetails("invalid project format, expected owner/repo",
			"project", project)
	}
	return parts[0], parts[1], nil
}
