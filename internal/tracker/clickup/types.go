package clickup

import "encoding/json"

// cuTask is the ClickUp API representation of a task.
type cuTask struct {
	ID                  string          `json:"id"`
	CustomID            string          `json:"custom_id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	TextContent         string          `json:"text_content"`
	Status              cuStatus        `json:"status"`
	Assignees           []cuUser        `json:"assignees"`
	Creator             cuUser          `json:"creator"`
	DateCreated         string          `json:"date_created"` // unix ms as string
	DateUpdated         string          `json:"date_updated"` // unix ms as string
	URL                 string          `json:"url"`
	List                cuListRef       `json:"list"`
	Priority            *cuPriority     `json:"priority"`
	Parent              string          `json:"parent"`               // parent task ID (subtask support)
	Tags                []cuTag         `json:"tags"`                 // task tags/labels
	CustomFields        []cuCustomField `json:"custom_fields"`        // custom field values
	MarkdownDescription string          `json:"markdown_description"` // markdown source (only with include_markdown_description=true)
}

// cuStatus is an embedded status within a task or list.
type cuStatus struct {
	Status     string      `json:"status"`
	Color      string      `json:"color"`
	Type       string      `json:"type"`       // "open", "custom", "done", "closed"
	OrderIndex json.Number `json:"orderindex"` // number in task responses, string in list responses
}

// cuUser is ClickUp user information.
type cuUser struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	Color          string `json:"color"`
	ProfilePicture string `json:"profilePicture"`
	Initials       string `json:"initials"`
}

// cuComment is a single task comment.
type cuComment struct {
	ID          string `json:"id"`
	CommentText string `json:"comment_text"`
	User        cuUser `json:"user"`
	Date        string `json:"date"` // unix ms as string
}

// cuListRef is a minimal list reference within a task.
type cuListRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cuListDetail is a full list detail including statuses.
type cuListDetail struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Statuses []cuStatus `json:"statuses"`
}

// cuTaskListResponse is the paginated task list response.
type cuTaskListResponse struct {
	Tasks    []cuTask `json:"tasks"`
	LastPage bool     `json:"last_page"`
}

// cuCommentListResponse is the comment list response.
type cuCommentListResponse struct {
	Comments []cuComment `json:"comments"`
}

// cuCurrentUser is the response from GET /v2/user.
type cuCurrentUser struct {
	User cuUser `json:"user"`
}

// cuPriority holds priority information for a task.
type cuPriority struct {
	ID         string      `json:"id"`
	Priority   string      `json:"priority"`
	Color      string      `json:"color"`
	OrderIndex json.Number `json:"orderindex"`
}

// cuTag is a tag/label on a task.
type cuTag struct {
	Name string `json:"name"`
}

// cuCustomField is a custom field value on a task.
type cuCustomField struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // "text", "dropdown", "labels", "date", "url", "number", "checkbox", etc.
	Value    any    `json:"value"`
	Required bool   `json:"required"`
}

// cuSpace is a ClickUp workspace space.
type cuSpace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cuSpacesResponse is the response from GET /api/v2/team/{team_id}/space.
type cuSpacesResponse struct {
	Spaces []cuSpace `json:"spaces"`
}

// cuFolder is a ClickUp folder within a space.
type cuFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cuFoldersResponse is the response from GET /api/v2/space/{space_id}/folder.
type cuFoldersResponse struct {
	Folders []cuFolder `json:"folders"`
}

// cuListFull is a ClickUp list within a folder or space.
type cuListFull struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cuListsResponse is the response from list endpoints.
type cuListsResponse struct {
	Lists []cuListFull `json:"lists"`
}

// cuTeamMember wraps a user within a team members response.
type cuTeamMember struct {
	User cuUser `json:"user"`
}

// cuTeamsResponse is the response from GET /api/v2/team.
type cuTeamsResponse struct {
	Teams []cuTeamDetail `json:"teams"`
}

// cuTeamDetail is a workspace with its members.
type cuTeamDetail struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Members []cuTeamMember `json:"members"`
}

// Space is a public ClickUp space representation.
type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Folder is a public ClickUp folder representation.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// List is a public ClickUp list representation.
type List struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Member is a public ClickUp workspace member representation.
type Member struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// CustomFieldValue is a public representation of a custom field value on a task.
type CustomFieldValue struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Value    any    `json:"value"`
	Required bool   `json:"required"`
}
