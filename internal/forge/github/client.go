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
	_ forge.Forge                = (*Client)(nil)
	_ forge.ChecksReader         = (*Client)(nil)
	_ forge.Merger               = (*Client)(nil)
	_ forge.MergedReader         = (*Client)(nil)
	_ forge.BranchDeleter        = (*Client)(nil)
	_ forge.PullRequestFinder    = (*Client)(nil)
	_ forge.PullRequestReader    = (*Client)(nil)
	_ forge.OpenWorkFinder       = (*Client)(nil)
	_ forge.ReadyForReviewMarker = (*Client)(nil)
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
		Draft: pr.Draft,
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
		Draft:  pr.Draft,
		Number: result.Number,
		URL:    result.HTMLURL,
	}, nil
}

// MarkReadyForReview implements forge.ReadyForReviewMarker. Un-drafting a PR is
// a GraphQL-only mutation on GitHub — the REST pulls endpoint cannot toggle the
// draft flag — so it resolves the PR's GraphQL node id and runs
// markPullRequestReadyForReview. GitHub returns HTTP 200 with an `errors` array
// on a GraphQL-level failure, so the body is inspected rather than the status.
func (c *Client) MarkReadyForReview(ctx context.Context, repoName string, number int) error {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return err
	}
	nodeID, err := c.pullNodeID(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	payload := graphQLRequest{
		Query:     "mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}",
		Variables: map[string]any{"id": nodeID},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling ready-for-review mutation", "repo", repoName, "number", number)
	}
	resp, err := c.api.Do(ctx, http.MethodPost, "/graphql", "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	var result graphQLResponse
	if err := apiclient.DecodeJSON(resp, &result, "repo", repoName, "number", number); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		// The reason goes into the message itself — the deploy pipeline surfaces
		// err.Error() on the board card, where structured details are invisible.
		return errors.WithDetails("marking pull request ready for review failed: "+result.Errors[0].Message,
			"repo", repoName, "number", number)
	}
	return nil
}

// pullNodeID fetches a pull request's GraphQL global id, needed by mutations the
// REST API cannot express.
func (c *Client) pullNodeID(ctx context.Context, owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.api.Do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return "", err
	}
	var pull pullGetResponse
	if err := apiclient.DecodeJSON(resp, &pull, "number", number); err != nil {
		return "", err
	}
	if pull.NodeID == "" {
		return "", errors.WithDetails("pull request has no node id", "number", number)
	}
	return pull.NodeID, nil
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

// FindOpenWork implements forge.OpenWorkFinder. It lists the repo's OPEN pull
// requests and its branches, and reports every one whose title, head ref, or
// branch name references key (case-insensitive substring). A branch already
// carried as a reported PR's head is not reported a second time. This is the
// live "already underway" signal preflight consults before starting a run so it
// never duplicates open work (SC-2648).
func (c *Client) FindOpenWork(ctx context.Context, repoName, key string) ([]forge.OpenWork, error) {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(key))
	var out []forge.OpenWork
	prHeads := map[string]bool{}

	pulls, err := c.listAllOpenPulls(ctx, owner, repo, repoName)
	if err != nil {
		return nil, err
	}
	for _, p := range pulls {
		if referencesKey(needle, p.Title) || referencesKey(needle, p.Head.Ref) {
			out = append(out, forge.OpenWork{
				Kind: "pull-request", Ref: p.Head.Ref, Title: p.Title,
				Number: p.Number, URL: p.HTMLURL,
			})
			prHeads[p.Head.Ref] = true
		}
	}

	branches, err := c.listAllBranches(ctx, owner, repo, repoName)
	if err != nil {
		return nil, err
	}
	for _, b := range branches {
		if referencesKey(needle, b.Name) && !prHeads[b.Name] {
			out = append(out, forge.OpenWork{Kind: "branch", Ref: b.Name})
		}
	}
	return out, nil
}

// referencesKey reports whether text names the ticket needle (already
// lowercased), case-insensitively.
//
// The match must not run into an adjacent alphanumeric on either side. Ticket
// keys are sequential, so plain substring matching makes every key a prefix of
// its later siblings: SC-264 would be "found" in the branch autofix/sc-2648 and
// stop a run because of a pull request belonging to a different ticket. Halting
// work over a collision that is not happening is precisely what this signal
// exists to stop doing.
func referencesKey(needle, text string) bool {
	if needle == "" {
		return false
	}
	hay := strings.ToLower(text)
	for at := 0; ; {
		i := strings.Index(hay[at:], needle)
		if i < 0 {
			return false
		}
		i += at
		end := i + len(needle)
		if !alnumAt(hay, i-1) && !alnumAt(hay, end) {
			return true
		}
		at = i + 1
	}
}

// alnumAt reports whether s has an ASCII letter or digit at i, treating an
// out-of-range index as a boundary.
func alnumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// listAllOpenPulls returns every open pull request, following pagination.
func (c *Client) listAllOpenPulls(ctx context.Context, owner, repo, repoName string) ([]pullListItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repo))
	query := url.Values{}
	query.Set("state", "open")
	query.Set("per_page", "100")
	var all []pullListItem
	raw := query.Encode()
	for raw != "" {
		resp, err := c.api.Do(ctx, http.MethodGet, path, raw, nil)
		if err != nil {
			return nil, err
		}
		next := nextPageQuery(resp)
		var page []pullListItem
		if err := apiclient.DecodeJSON(resp, &page, "repo", repoName); err != nil {
			return nil, err
		}
		all = append(all, page...)
		raw = next
	}
	return all, nil
}

// listAllBranches returns every branch, following pagination.
func (c *Client) listAllBranches(ctx context.Context, owner, repo, repoName string) ([]branchListItem, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches", url.PathEscape(owner), url.PathEscape(repo))
	query := url.Values{}
	query.Set("per_page", "100")
	var all []branchListItem
	raw := query.Encode()
	for raw != "" {
		resp, err := c.api.Do(ctx, http.MethodGet, path, raw, nil)
		if err != nil {
			return nil, err
		}
		next := nextPageQuery(resp)
		var page []branchListItem
		if err := apiclient.DecodeJSON(resp, &page, "repo", repoName); err != nil {
			return nil, err
		}
		all = append(all, page...)
		raw = next
	}
	return all, nil
}

// nextPageQuery extracts the raw query string of the GitHub Link header's
// rel="next" URL, or "" when there is no next page. Read the header BEFORE
// DecodeJSON consumes the response body.
func nextPageQuery(resp *http.Response) string {
	for _, part := range strings.Split(resp.Header.Get("Link"), ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		if strings.TrimSpace(segs[1]) != `rel="next"` {
			continue
		}
		rawURL := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		if u, err := url.Parse(rawURL); err == nil {
			return u.RawQuery
		}
	}
	return ""
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
//
// A "cancelled" conclusion is a non-answer, not a "no": cancel-in-progress
// concurrency deliberately calls off a superseded build when a branch is
// rebased, so a cancelled run is treated as non-conclusive (pending) and the
// gate keeps waiting for the run that matters rather than misreading routine
// housekeeping as a red build (SC-2602). deployTimeout bounds a build a human
// stopped for good. Runs are judged latest-per-name so a passed re-run overrides
// the cancelled attempt it replaced.
func combineChecks(runs checkRunsResponse, combined combinedStatusResponse) forge.ChecksState {
	state := forge.ChecksPassing
	for _, run := range latestRunPerName(runs.CheckRuns) {
		switch runVerdict(run) {
		case forge.ChecksFailing:
			return forge.ChecksFailing
		case forge.ChecksPending:
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

// runVerdict folds one check run into the forge's three-state vocabulary, the
// single mapping combineChecks and the per-check reader both use so an aggregate
// verdict and a per-check list can never disagree. Cancelled is non-conclusive
// (SC-2602): a superseded build is called off, not failed.
func runVerdict(run checkRun) forge.ChecksState {
	switch run.Conclusion {
	case "failure", "timed_out", "action_required":
		return forge.ChecksFailing
	case "cancelled":
		return forge.ChecksPending
	}
	if run.Status != "completed" {
		return forge.ChecksPending
	}
	return forge.ChecksPassing
}

// statusVerdict folds one legacy commit-status state into the same vocabulary.
func statusVerdict(state string) forge.ChecksState {
	switch state {
	case "failure", "error":
		return forge.ChecksFailing
	case "pending":
		return forge.ChecksPending
	}
	return forge.ChecksPassing
}

// ReadPullRequest implements forge.PullRequestReader: one pull GET for identity
// and mergeability, then the two CI systems for per-check results — the full
// read surface the deploy gate's ChecksReader collapses into a single word.
func (c *Client) ReadPullRequest(ctx context.Context, repoName string, number int) (*forge.PullRequestState, error) {
	owner, repo, err := splitProject(repoName)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	resp, err := c.api.Do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	var pull pullGetResponse
	if err := apiclient.DecodeJSON(resp, &pull, "number", number); err != nil {
		return nil, err
	}
	if pull.Head.SHA == "" {
		return nil, errors.WithDetails("pull request has no head SHA", "number", number)
	}
	checks, err := c.checkResults(ctx, owner, repo, repoName, pull.Head.SHA)
	if err != nil {
		return nil, err
	}
	return &forge.PullRequestState{
		Number:    number,
		HeadRef:   pull.Head.Ref,
		BaseRef:   pull.Base.Ref,
		HeadSHA:   pull.Head.SHA,
		Mergeable: pull.Mergeable != nil && *pull.Mergeable,
		Checks:    checks,
	}, nil
}

// checkResults reads both CI systems for a head SHA and returns one entry per
// check — check runs deduped to the latest attempt per name (SC-2602), then each
// legacy commit-status context.
func (c *Client) checkResults(ctx context.Context, owner, repo, repoName, sha string) ([]forge.CheckResult, error) {
	runsPath := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	resp, err := c.api.Do(ctx, http.MethodGet, runsPath, "", nil)
	if err != nil {
		return nil, err
	}
	var runs checkRunsResponse
	if err := apiclient.DecodeJSON(resp, &runs, "repo", repoName); err != nil {
		return nil, err
	}
	statusPath := fmt.Sprintf("/repos/%s/%s/commits/%s/status",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))
	resp, err = c.api.Do(ctx, http.MethodGet, statusPath, "", nil)
	if err != nil {
		return nil, err
	}
	var combined combinedStatusResponse
	if err := apiclient.DecodeJSON(resp, &combined, "repo", repoName); err != nil {
		return nil, err
	}

	var results []forge.CheckResult
	for _, run := range latestRunPerName(runs.CheckRuns) {
		results = append(results, forge.CheckResult{
			Name:       run.Name,
			Conclusion: runVerdict(run),
			DetailsURL: run.DetailsURL,
		})
	}
	for _, s := range combined.Statuses {
		results = append(results, forge.CheckResult{
			Name:       s.Context,
			Conclusion: statusVerdict(s.State),
			DetailsURL: s.TargetURL,
		})
	}
	return results, nil
}

// latestRunPerName keeps only the most recent attempt of each named check, so a
// superseded (cancelled) attempt does not overrule the re-run that replaced it.
// GitHub returns every attempt for a head SHA; started_at (RFC3339 UTC) orders
// them, ties keeping the later element. Runs with no name (degenerate payloads)
// are kept as distinct entries so they cannot collapse into one another.
func latestRunPerName(runs []checkRun) []checkRun {
	indexByName := map[string]int{}
	result := make([]checkRun, 0, len(runs))
	for _, run := range runs {
		if run.Name == "" {
			result = append(result, run)
			continue
		}
		if i, ok := indexByName[run.Name]; ok {
			if run.StartedAt >= result[i].StartedAt {
				result[i] = run
			}
			continue
		}
		indexByName[run.Name] = len(result)
		result = append(result, run)
	}
	return result
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

// PullRequestMerged implements forge.MergedReader via the pulls GET endpoint's
// top-level "merged" flag — true once the PR has landed on its base, whether
// merged by the deploy pipeline or by hand.
func (c *Client) PullRequestMerged(ctx context.Context, repoName string, number int) (bool, error) {
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
	return pull.Merged, nil
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
