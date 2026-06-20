package linear

// linearIssue is the Linear API representation of an issue.
type linearIssue struct {
	Identifier    string          `json:"identifier"`
	URL           string          `json:"url"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	State         stateNode       `json:"state"`
	PriorityLabel string          `json:"priorityLabel"`
	Assignee      *nameNode       `json:"assignee"`
	Creator       *nameNode       `json:"creator"`
	Labels        labelConnection `json:"labels"`
	UpdatedAt     string          `json:"updatedAt"`
	Parent        *identifierNode `json:"parent"`
}

// identifierNode holds a related issue's human-facing identifier (e.g. "ENG-12").
type identifierNode struct {
	Identifier string `json:"identifier"`
}

type nameNode struct {
	Name string `json:"name"`
}

// stateNode holds a workflow state's name and type (e.g., "unstarted", "started", "completed").
type stateNode struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type labelConnection struct {
	Nodes []nameNode `json:"nodes"`
}

// Response wrappers for specific queries.

type issuesData struct {
	Issues issueConnection `json:"issues"`
}

type issueConnection struct {
	Nodes []linearIssue `json:"nodes"`
}

type issueData struct {
	Issue linearIssue `json:"issue"`
}

type teamsData struct {
	Teams teamConnection `json:"teams"`
}

type teamConnection struct {
	Nodes []teamNode `json:"nodes"`
}

type teamNode struct {
	ID string `json:"id"`
}

type issueCreateData struct {
	IssueCreate issueCreatePayload `json:"issueCreate"`
}

type issueCreatePayload struct {
	Success bool        `json:"success"`
	Issue   linearIssue `json:"issue"`
}

type linearComment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	User      *nameNode `json:"user"`
	CreatedAt string    `json:"createdAt"`
}

type commentCreateData struct {
	CommentCreate struct {
		Success bool          `json:"success"`
		Comment linearComment `json:"comment"`
	} `json:"commentCreate"`
}

type issueCommentsData struct {
	Issue struct {
		Comments struct {
			Nodes []linearComment `json:"nodes"`
		} `json:"comments"`
	} `json:"issue"`
}

type issueIDData struct {
	Issue struct {
		ID string `json:"id"`
	} `json:"issue"`
}

type issueDeleteData struct {
	IssueDelete struct {
		Success bool `json:"success"`
	} `json:"issueDelete"`
}

// linearState represents a workflow state in Linear.
type linearState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type teamStatesData struct {
	Teams struct {
		Nodes []struct {
			ID     string `json:"id"`
			States struct {
				Nodes []linearState `json:"nodes"`
			} `json:"states"`
		} `json:"nodes"`
	} `json:"teams"`
}

type issueUpdateData struct {
	IssueUpdate struct {
		Success bool `json:"success"`
	} `json:"issueUpdate"`
}

type viewerData struct {
	Viewer struct {
		ID string `json:"id"`
	} `json:"viewer"`
}

type projectsData struct {
	Projects projectConnection `json:"projects"`
}

type projectConnection struct {
	Nodes []projectNode `json:"nodes"`
}

type projectNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
