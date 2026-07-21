package github

type pullCreateRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body,omitempty"`
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
