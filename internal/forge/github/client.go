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

var _ forge.Forge = (*Client)(nil)

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
