package github

type pullCreateRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body,omitempty"`
	// Draft opens the PR in GitHub's draft state — unmergeable until marked
	// ready. The review→fix loop opens draft so a half-reviewed PR cannot be
	// merged even if the daemon's own gate had a bug.
	Draft bool `json:"draft,omitempty"`
}

type pullCreateResponse struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
}

type pullGetResponse struct {
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	// Mergeable is GitHub's end-state merge verdict. It is null while GitHub
	// computes it asynchronously; a nil pointer is treated as not-yet-mergeable.
	Mergeable *bool `json:"mergeable"`
	// Merged reports whether the PR has already been merged into its base.
	Merged bool `json:"merged"`
	// NodeID is the PR's GraphQL global id, needed by the ready-for-review
	// mutation (which the REST API cannot express).
	NodeID string `json:"node_id"`
}

// graphQLRequest is a GraphQL query/mutation POST body. Used for the operations
// GitHub exposes only through GraphQL — marking a draft PR ready for review.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphQLResponse carries the errors array a GraphQL endpoint returns with an
// HTTP 200 even when the operation failed, so a caller must inspect it.
type graphQLResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

type checkRunsResponse struct {
	CheckRuns []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"check_runs"`
}

type combinedStatusResponse struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

type mergeRequest struct {
	MergeMethod string `json:"merge_method"`
}

type mergeResponse struct {
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// pullListItem is one element of the GitHub list-pulls response used to adopt
// an existing open PR for a head branch (SC-989).
type pullListItem struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
}
