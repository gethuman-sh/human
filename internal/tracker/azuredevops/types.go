package azuredevops

// adoWorkItem is the Azure DevOps API representation of a work item.
type adoWorkItem struct {
	ID        int           `json:"id"`
	Rev       int           `json:"rev"`
	Fields    adoFields     `json:"fields"`
	URL       string        `json:"url"`
	Relations []adoRelation `json:"relations"` // present only with $expand=relations
}

// adoRelation is a link between work items (e.g. parent/child hierarchy).
type adoRelation struct {
	Rel string `json:"rel"`
	URL string `json:"url"`
}

// adoFields contains the System.* and Microsoft.* fields of a work item.
type adoFields struct {
	Title        string          `json:"System.Title"`
	Description  string          `json:"System.Description"`
	State        string          `json:"System.State"`
	WorkItemType string          `json:"System.WorkItemType"`
	AssignedTo   *adoIdentityRef `json:"System.AssignedTo"`
	CreatedBy    *adoIdentityRef `json:"System.CreatedBy"`
	Priority     int             `json:"Microsoft.VSTS.Common.Priority"`
	TeamProject  string          `json:"System.TeamProject"`
	ChangedDate  string          `json:"System.ChangedDate"`
}

// adoIdentityRef is an Azure DevOps identity reference.
type adoIdentityRef struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

// adoWIQLResponse is the response from a WIQL query.
type adoWIQLResponse struct {
	WorkItems []adoWIQLRef `json:"workItems"`
}

// adoWIQLRef is a work item reference returned by WIQL.
type adoWIQLRef struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// adoComment is a single work item comment.
type adoComment struct {
	ID          int             `json:"id"`
	Text        string          `json:"text"`
	CreatedBy   *adoIdentityRef `json:"createdBy"`
	CreatedDate string          `json:"createdDate"`
}

// adoCommentList is the response from the comments endpoint.
type adoCommentList struct {
	Comments []adoComment `json:"comments"`
}

// patchOp is a single JSON Patch operation for creating/updating work items.
type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// adoConnectionData is the response from the /_apis/connectionData endpoint.
type adoConnectionData struct {
	AuthenticatedUser adoIdentityRef `json:"authenticatedUser"`
}

// adoWorkItemTypeStatesResponse is the response from the work item type states endpoint.
type adoWorkItemTypeStatesResponse struct {
	Value []adoWorkItemTypeState `json:"value"`
}

// adoWorkItemTypeState represents a single state for a work item type.
type adoWorkItemTypeState struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}
