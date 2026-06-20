package notion

// notionPage represents a Notion page from the API.
type notionPage struct {
	Object     string                    `json:"object"`
	ID         string                    `json:"id"`
	URL        string                    `json:"url"`
	Parent     notionParent              `json:"parent"`
	Properties map[string]notionProperty `json:"properties"`
}

// notionParent represents the parent of a Notion object.
type notionParent struct {
	Type       string `json:"type"`
	DatabaseID string `json:"database_id,omitempty"`
	PageID     string `json:"page_id,omitempty"`
	Workspace  bool   `json:"workspace,omitempty"`
}

// notionProperty represents a Notion property value.
type notionProperty struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Title    []notionRichText `json:"title,omitempty"`
	RichText []notionRichText `json:"rich_text,omitempty"`
	Number   *float64         `json:"number,omitempty"`
	Select   *selectOption    `json:"select,omitempty"`
	URL      *string          `json:"url,omitempty"`
	Checkbox *bool            `json:"checkbox,omitempty"`
	Date     *dateValue       `json:"date,omitempty"`
	Email    *string          `json:"email,omitempty"`
	Phone    *string          `json:"phone_number,omitempty"`
	Status   *selectOption    `json:"status,omitempty"`
}

type selectOption struct {
	Name string `json:"name"`
}

type dateValue struct {
	Start string `json:"start"`
	End   string `json:"end,omitempty"`
}

// notionRichText represents a rich text element.
type notionRichText struct {
	Type        string            `json:"type"`
	Text        notionTextObj     `json:"text"`
	Annotations notionAnnotations `json:"annotations"`
	PlainText   string            `json:"plain_text"`
	Href        *string           `json:"href"`
}

// notionTextObj holds the text content and optional link.
type notionTextObj struct {
	Content string      `json:"content"`
	Link    *notionLink `json:"link"`
}

type notionLink struct {
	URL string `json:"url"`
}

// notionAnnotations holds rich text formatting.
type notionAnnotations struct {
	Bold          bool `json:"bold"`
	Italic        bool `json:"italic"`
	Strikethrough bool `json:"strikethrough"`
	Code          bool `json:"code"`
}

// notionBlock represents a Notion block.
type notionBlock struct {
	Object      string `json:"object"`
	ID          string `json:"id"`
	Type        string `json:"type"`
	HasChildren bool   `json:"has_children"`

	Paragraph        *textBlock      `json:"paragraph,omitempty"`
	Heading1         *textBlock      `json:"heading_1,omitempty"`
	Heading2         *textBlock      `json:"heading_2,omitempty"`
	Heading3         *textBlock      `json:"heading_3,omitempty"`
	BulletedListItem *textBlock      `json:"bulleted_list_item,omitempty"`
	NumberedListItem *textBlock      `json:"numbered_list_item,omitempty"`
	ToDo             *toDoBlock      `json:"to_do,omitempty"`
	Code             *codeBlock      `json:"code,omitempty"`
	Quote            *textBlock      `json:"quote,omitempty"`
	Divider          *struct{}       `json:"divider,omitempty"`
	Callout          *calloutBlock   `json:"callout,omitempty"`
	Image            *fileBlock      `json:"image,omitempty"`
	Bookmark         *bookmarkBlock  `json:"bookmark,omitempty"`
	ChildPage        *childPageBlock `json:"child_page,omitempty"`
	Table            *tableBlock     `json:"table,omitempty"`
	TableRow         *tableRowBlock  `json:"table_row,omitempty"`

	Children []notionBlock `json:"-"` // populated by recursive fetch
}

// headingBlock returns the textBlock for whichever heading type is set.
func (b notionBlock) headingBlock() *textBlock {
	if b.Heading1 != nil {
		return b.Heading1
	}
	if b.Heading2 != nil {
		return b.Heading2
	}
	return b.Heading3
}

type textBlock struct {
	RichText []notionRichText `json:"rich_text"`
}

type toDoBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Checked  bool             `json:"checked"`
}

type codeBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Language string           `json:"language"`
}

type calloutBlock struct {
	RichText []notionRichText `json:"rich_text"`
	Icon     *notionIcon      `json:"icon,omitempty"`
}

type notionIcon struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji,omitempty"`
}

type fileBlock struct {
	Type     string           `json:"type"`
	File     *fileURL         `json:"file,omitempty"`
	External *fileURL         `json:"external,omitempty"`
	Caption  []notionRichText `json:"caption,omitempty"`
}

type fileURL struct {
	URL string `json:"url"`
}

type bookmarkBlock struct {
	URL     string           `json:"url"`
	Caption []notionRichText `json:"caption,omitempty"`
}

type childPageBlock struct {
	Title string `json:"title"`
}

type tableBlock struct {
	TableWidth      int  `json:"table_width"`
	HasColumnHeader bool `json:"has_column_header"`
	HasRowHeader    bool `json:"has_row_header"`
}

type tableRowBlock struct {
	Cells [][]notionRichText `json:"cells"`
}

// notionDatabase represents a Notion database from the API.
type notionDatabase struct {
	Object     string                  `json:"object"`
	ID         string                  `json:"id"`
	URL        string                  `json:"url"`
	Title      []notionRichText        `json:"title"`
	Properties map[string]notionDBProp `json:"properties"`
}

// notionDBProp describes a database property schema.
type notionDBProp struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// paginatedResponse is a generic paginated response from the Notion API.
type paginatedResponse[T any] struct {
	Object     string `json:"object"`
	Results    []T    `json:"results"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// searchRequest is the body for POST /v1/search.
type searchRequest struct {
	Query       string        `json:"query,omitempty"`
	Filter      *searchFilter `json:"filter,omitempty"`
	Sort        *searchSort   `json:"sort,omitempty"`
	PageSize    int           `json:"page_size,omitempty"`
	StartCursor string        `json:"start_cursor,omitempty"`
}

type searchFilter struct {
	Value    string `json:"value"`
	Property string `json:"property"`
}

type searchSort struct {
	Direction string `json:"direction"`
	Timestamp string `json:"timestamp"`
}

// databaseQueryRequest is the body for POST /v1/databases/{id}/query.
type databaseQueryRequest struct {
	PageSize    int    `json:"page_size,omitempty"`
	StartCursor string `json:"start_cursor,omitempty"`
}

// --- Output types ---

// SearchResult is a search result for CLI output.
type SearchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

// DatabaseEntry is a database listing entry for CLI output.
type DatabaseEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// DatabaseRow is a single row from a database query for CLI output.
type DatabaseRow struct {
	ID         string            `json:"id"`
	URL        string            `json:"url"`
	Properties map[string]string `json:"properties"`
}
