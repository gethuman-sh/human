package figma

// --- Private API types (JSON deserialization) ---

// figmaFile is the response from GET /v1/files/:key.
type figmaFile struct {
	Name         string                        `json:"name"`
	LastModified string                        `json:"lastModified"`
	ThumbnailURL string                        `json:"thumbnailUrl"`
	Version      string                        `json:"version"`
	Document     figmaNode                     `json:"document"`
	Components   map[string]figmaComponentMeta `json:"components"`
}

// figmaNode represents a node in the Figma document tree.
type figmaNode struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Type                string            `json:"type"`
	Children            []figmaNode       `json:"children,omitempty"`
	AbsoluteBoundingBox *figmaBoundingBox `json:"absoluteBoundingBox,omitempty"`
	Fills               []figmaPaint      `json:"fills,omitempty"`
	Strokes             []figmaPaint      `json:"strokes,omitempty"`
	Characters          string            `json:"characters,omitempty"`
	Style               *figmaTextStyle   `json:"style,omitempty"`
	ComponentID         string            `json:"componentId,omitempty"`
}

// figmaBoundingBox represents node dimensions.
type figmaBoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// figmaPaint represents a fill or stroke.
type figmaPaint struct {
	Type    string      `json:"type"`
	Color   *figmaColor `json:"color,omitempty"`
	Visible *bool       `json:"visible,omitempty"`
}

// figmaColor represents an RGBA color.
type figmaColor struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
	A float64 `json:"a"`
}

// figmaTextStyle represents text styling properties.
type figmaTextStyle struct {
	FontFamily string  `json:"fontFamily,omitempty"`
	FontSize   float64 `json:"fontSize,omitempty"`
	FontWeight float64 `json:"fontWeight,omitempty"`
}

// figmaComponentMeta holds component metadata from the file response.
type figmaComponentMeta struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// figmaNodesResponse is the response from GET /v1/files/:key/nodes.
type figmaNodesResponse struct {
	Nodes map[string]figmaNodeEntry `json:"nodes"`
}

// figmaNodeEntry wraps a single node in the nodes response.
type figmaNodeEntry struct {
	Document figmaNode `json:"document"`
}

// figmaComment represents a comment on a file.
type figmaComment struct {
	ID         string           `json:"id"`
	Message    string           `json:"message"`
	CreatedAt  string           `json:"created_at"`
	ResolvedAt *string          `json:"resolved_at,omitempty"`
	User       figmaUser        `json:"user"`
	ClientMeta *figmaClientMeta `json:"client_meta,omitempty"`
	OrderID    *string          `json:"order_id,omitempty"`
	ParentID   string           `json:"parent_id,omitempty"`
}

// figmaUser represents a Figma user.
type figmaUser struct {
	Handle string `json:"handle"`
	ImgURL string `json:"img_url"`
}

// figmaClientMeta holds position metadata for a comment.
type figmaClientMeta struct {
	NodeID     string `json:"node_id,omitempty"`
	NodeOffset *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"node_offset,omitempty"`
}

// figmaCommentsResponse is the response from GET /v1/files/:key/comments.
type figmaCommentsResponse struct {
	Comments []figmaComment `json:"comments"`
}

// figmaImagesResponse is the response from GET /v1/images/:key.
type figmaImagesResponse struct {
	Images map[string]string `json:"images"`
}

// figmaProject represents a team project.
type figmaProject struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// figmaProjectsResponse is the response from GET /v1/teams/:id/projects.
type figmaProjectsResponse struct {
	Projects []figmaProject `json:"projects"`
}

// figmaProjectFile represents a file within a project.
type figmaProjectFile struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	ThumbnailURL string `json:"thumbnail_url"`
	LastModified string `json:"last_modified"`
}

// figmaProjectFilesResponse is the response from GET /v1/projects/:id/files.
type figmaProjectFilesResponse struct {
	Files []figmaProjectFile `json:"files"`
}

// figmaFileComponentsResponse is the response from GET /v1/files/:key/components.
type figmaFileComponentsResponse struct {
	Meta figmaComponentsMeta `json:"meta"`
}

// figmaComponentsMeta holds the components array and cursor.
type figmaComponentsMeta struct {
	Components []figmaComponentDetail `json:"components"`
	Cursor     *string                `json:"cursor,omitempty"`
}

// figmaComponentDetail represents a published component.
type figmaComponentDetail struct {
	Key             string               `json:"key"`
	NodeID          string               `json:"node_id"`
	Name            string               `json:"name"`
	Description     string               `json:"description"`
	ContainingFrame figmaContainingFrame `json:"containing_frame"`
}

// figmaContainingFrame describes the frame that contains a component.
type figmaContainingFrame struct {
	Name     string `json:"name"`
	NodeID   string `json:"nodeId"`
	PageID   string `json:"pageId"`
	PageName string `json:"pageName"`
}

// --- Exported output types (AI-friendly) ---

// FileSummary is the output for file get.
type FileSummary struct {
	Name           string        `json:"name"`
	LastModified   string        `json:"last_modified"`
	ThumbnailURL   string        `json:"thumbnail_url"`
	Version        string        `json:"version"`
	Pages          []PageSummary `json:"pages"`
	ComponentCount int           `json:"component_count"`
}

// PageSummary describes a top-level page in a Figma file.
type PageSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ChildCount int    `json:"child_count"`
}

// NodeSummary is a clean representation of a Figma node.
type NodeSummary struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Size        *Size         `json:"size,omitempty"`
	Text        string        `json:"text,omitempty"`
	Typography  *Typography   `json:"typography,omitempty"`
	Fills       []string      `json:"fills,omitempty"`
	Children    []NodeSummary `json:"children,omitempty"`
	ComponentID string        `json:"component_id,omitempty"`
}

// Size represents width/height dimensions.
type Size struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Typography holds font properties for text nodes.
type Typography struct {
	FontFamily string  `json:"font_family"`
	FontSize   float64 `json:"font_size"`
	FontWeight float64 `json:"font_weight"`
}

// Component represents a published component.
type Component struct {
	Key         string `json:"key"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Page        string `json:"page"`
	Frame       string `json:"frame"`
}

// FileComment represents a comment on a file.
type FileComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	Resolved  bool   `json:"resolved"`
	NodeID    string `json:"node_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
}

// ImageExport represents an exported node image.
type ImageExport struct {
	NodeID string `json:"node_id"`
	URL    string `json:"url"`
}

// Project represents a Figma team project.
type Project struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ProjectFile represents a file in a project.
type ProjectFile struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	ThumbnailURL string `json:"thumbnail_url"`
	LastModified string `json:"last_modified"`
}
