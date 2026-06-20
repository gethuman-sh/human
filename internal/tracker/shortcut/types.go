package shortcut

// scStory is the Shortcut API representation of a story.
type scStory struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	StoryType       string   `json:"story_type"`
	WorkflowStateID int64    `json:"workflow_state_id"`
	AppURL          string   `json:"app_url"`
	OwnerIDs        []string `json:"owner_ids"`
	RequestedByID   string   `json:"requested_by_id"`
	Archived        bool     `json:"archived"`
	ProjectID       *int64   `json:"project_id"`
	GroupID         string   `json:"group_id"`        // UUID of the group (team)
	ParentStoryID   *int64   `json:"parent_story_id"` // set when this story is a subtask
	UpdatedAt       string   `json:"updated_at"`
}

// scSearchRequest is the request body for POST /api/v3/stories/search.
type scSearchRequest struct {
	GroupIDs       []string `json:"group_ids,omitempty"`
	UpdatedAtStart string   `json:"updated_at_start,omitempty"`
	// Archived filters by archived status. Always set to false when searching
	// without other filters so the body is non-empty — sending {} returns no
	// results on some Shortcut workspaces.
	Archived *bool `json:"archived,omitempty"`
}

// scComment is a single story comment.
type scComment struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	AuthorID  string `json:"author_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// scMember is a Shortcut workspace member.
type scMember struct {
	ID      string    `json:"id"`
	Profile scProfile `json:"profile"`
}

// scProfile contains a member's display information.
type scProfile struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// scWorkflow is a Shortcut workflow containing states.
type scWorkflow struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	States []scWorkflowState `json:"states"`
}

// scWorkflowState is a single state within a workflow.
type scWorkflowState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "unstarted", "started", "done"
}

// scGroup is a Shortcut group (team), used for name → ID resolution.
type scGroup struct {
	ID   string `json:"id"` // UUID
	Name string `json:"name"`
}

// scMemberInfo is the response from /api/v3/member-info.
type scMemberInfo struct {
	ID string `json:"id"` // UUID
}
