package gitlab

// glIssue is the GitLab API representation of an issue.
type glIssue struct {
	IID         int          `json:"iid"`
	ProjectID   int          `json:"project_id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	State       string       `json:"state"`
	Author      *glUser      `json:"author"`
	Assignees   []glUser     `json:"assignees"`
	Labels      []string     `json:"labels"`
	UpdatedAt   string       `json:"updated_at"`
	References  *glReference `json:"references,omitempty"` // included in global /issues endpoint
	WebURL      string       `json:"web_url,omitempty"`
}

// glReference contains issue references from the GitLab API.
type glReference struct {
	Full string `json:"full"` // e.g. "group/project#123"
}

// glUser is the GitLab API representation of a user.
type glUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

// glNote is the GitLab API representation of a note (comment).
type glNote struct {
	ID        int     `json:"id"`
	Body      string  `json:"body"`
	Author    *glUser `json:"author"`
	CreatedAt string  `json:"created_at"`
	System    bool    `json:"system"`
}

type createRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type createResponse struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebURL      string `json:"web_url"`
}

type noteRequest struct {
	Body string `json:"body"`
}

type glCurrentUser struct {
	ID int `json:"id"`
}
