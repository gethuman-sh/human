package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
)

const notionVersion = "2022-06-28"

// Client is a Notion API client.
type Client struct {
	api *apiclient.Client
}

// New creates a Notion client with the given base URL and integration token.
func New(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BearerToken(token)),
			apiclient.WithURLBuilder(apiclient.ParsePathURL()),
			apiclient.WithHeader("Notion-Version", notionVersion),
			apiclient.WithContentType("application/json"),
			apiclient.WithProviderName("notion"),
		),
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// Search searches the Notion workspace for pages and databases matching the
// query, following pagination so workspaces with more than one page of results
// are returned in full (a truncated result set also drives index pruning).
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var results []SearchResult
	cursor := ""
	for {
		body, err := json.Marshal(searchRequest{
			Query:       query,
			PageSize:    100,
			StartCursor: cursor,
		})
		if err != nil {
			return nil, errors.WrapWithDetails(err, "marshalling search request")
		}

		resp, err := c.doRequest(ctx, http.MethodPost, "/v1/search", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		var paginated paginatedResponse[json.RawMessage]
		if err := apiclient.DecodeJSON(resp, &paginated); err != nil {
			return nil, err
		}

		for _, raw := range paginated.Results {
			var obj struct {
				Object     string                    `json:"object"`
				ID         string                    `json:"id"`
				URL        string                    `json:"url"`
				Properties map[string]notionProperty `json:"properties"`
				Title      []notionRichText          `json:"title"`
			}
			if err := json.Unmarshal(raw, &obj); err != nil {
				continue
			}

			title := extractTitle(obj.Object, obj.Properties, obj.Title)
			results = append(results, SearchResult{
				ID:    obj.ID,
				Title: title,
				URL:   obj.URL,
				Type:  obj.Object,
			})
		}

		if !paginated.HasMore || paginated.NextCursor == "" {
			break
		}
		cursor = paginated.NextCursor
	}
	return results, nil
}

// GetPage fetches a page's content and returns it as markdown.
func (c *Client) GetPage(ctx context.Context, pageID string) (string, error) {
	// Fetch page metadata for the title.
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/pages/"+url.PathEscape(pageID), nil)
	if err != nil {
		return "", err
	}
	var page notionPage
	if err := apiclient.DecodeJSON(resp, &page, "pageID", pageID); err != nil {
		return "", err
	}

	title := extractTitle("page", page.Properties, nil)

	// Fetch block children.
	blocks, err := c.getBlockChildren(ctx, pageID, 0)
	if err != nil {
		return "", err
	}

	md := ""
	if title != "" {
		md = "# " + title + "\n\n"
	}
	md += BlocksToMarkdown(blocks)
	return md, nil
}

// QueryDatabase queries a Notion database and returns its rows, following
// pagination so databases with more than one page of rows are returned in full.
func (c *Client) QueryDatabase(ctx context.Context, dbID string) ([]DatabaseRow, error) {
	var rows []DatabaseRow
	cursor := ""
	for {
		body, err := json.Marshal(databaseQueryRequest{PageSize: 100, StartCursor: cursor})
		if err != nil {
			return nil, errors.WrapWithDetails(err, "marshalling database query request")
		}

		resp, err := c.doRequest(ctx, http.MethodPost, "/v1/databases/"+url.PathEscape(dbID)+"/query", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		var paginated paginatedResponse[notionPage]
		if err := apiclient.DecodeJSON(resp, &paginated, "databaseID", dbID); err != nil {
			return nil, err
		}

		for _, page := range paginated.Results {
			props := make(map[string]string)
			for name, prop := range page.Properties {
				props[name] = propertyToString(prop)
			}
			rows = append(rows, DatabaseRow{
				ID:         page.ID,
				URL:        page.URL,
				Properties: props,
			})
		}

		if !paginated.HasMore || paginated.NextCursor == "" {
			break
		}
		cursor = paginated.NextCursor
	}
	return rows, nil
}

// ListDatabases lists all databases shared with the integration, following
// pagination so more than one page of databases is returned in full.
func (c *Client) ListDatabases(ctx context.Context) ([]DatabaseEntry, error) {
	var entries []DatabaseEntry
	cursor := ""
	for {
		body, err := json.Marshal(searchRequest{
			Filter: &searchFilter{
				Value:    "database",
				Property: "object",
			},
			PageSize:    100,
			StartCursor: cursor,
		})
		if err != nil {
			return nil, errors.WrapWithDetails(err, "marshalling databases list request")
		}

		resp, err := c.doRequest(ctx, http.MethodPost, "/v1/search", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		var paginated paginatedResponse[notionDatabase]
		if err := apiclient.DecodeJSON(resp, &paginated); err != nil {
			return nil, err
		}

		for _, db := range paginated.Results {
			title := richTextPlain(db.Title)
			entries = append(entries, DatabaseEntry{
				ID:    db.ID,
				Title: title,
				URL:   db.URL,
			})
		}

		if !paginated.HasMore || paginated.NextCursor == "" {
			break
		}
		cursor = paginated.NextCursor
	}
	return entries, nil
}

const maxBlockDepth = 3

// getBlockChildren fetches all child blocks of a block, with recursive fetching
// for nested blocks up to maxBlockDepth.
func (c *Client) getBlockChildren(ctx context.Context, blockID string, depth int) ([]notionBlock, error) {
	var allBlocks []notionBlock
	cursor := ""

	for {
		path := "/v1/blocks/" + url.PathEscape(blockID) + "/children?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + url.QueryEscape(cursor)
		}

		resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var paginated paginatedResponse[notionBlock]
		if err := apiclient.DecodeJSON(resp, &paginated, "blockID", blockID); err != nil {
			return nil, err
		}

		for i := range paginated.Results {
			block := &paginated.Results[i]
			if block.HasChildren && depth < maxBlockDepth {
				children, err := c.getBlockChildren(ctx, block.ID, depth+1)
				if err != nil {
					return nil, err
				}
				block.Children = children
			}
			allBlocks = append(allBlocks, *block)
		}

		if !paginated.HasMore {
			break
		}
		cursor = paginated.NextCursor
	}

	return allBlocks, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.api.Do(ctx, method, path, "", body)
}

// extractTitle extracts a title string from page properties or database title.
func extractTitle(objType string, properties map[string]notionProperty, dbTitle []notionRichText) string {
	if objType == "database" && len(dbTitle) > 0 {
		return richTextPlain(dbTitle)
	}
	for _, prop := range properties {
		if prop.Type == "title" && len(prop.Title) > 0 {
			return richTextPlain(prop.Title)
		}
	}
	return ""
}

// propertyConverters maps property types to their string conversion functions.
var propertyConverters = map[string]func(notionProperty) string{
	"title":        func(p notionProperty) string { return richTextPlain(p.Title) },
	"rich_text":    func(p notionProperty) string { return richTextPlain(p.RichText) },
	"number":       convertNumber,
	"select":       convertSelect,
	"status":       convertStatus,
	"url":          func(p notionProperty) string { return derefString(p.URL) },
	"checkbox":     convertCheckbox,
	"date":         convertDate,
	"email":        func(p notionProperty) string { return derefString(p.Email) },
	"phone_number": func(p notionProperty) string { return derefString(p.Phone) },
}

// propertyToString converts a property value to a display string.
func propertyToString(prop notionProperty) string {
	if fn, ok := propertyConverters[prop.Type]; ok {
		return fn(prop)
	}
	return ""
}

func convertNumber(p notionProperty) string {
	if p.Number != nil {
		return fmt.Sprintf("%g", *p.Number)
	}
	return ""
}

func convertSelect(p notionProperty) string {
	if p.Select != nil {
		return p.Select.Name
	}
	return ""
}

func convertStatus(p notionProperty) string {
	if p.Status != nil {
		return p.Status.Name
	}
	return ""
}

func convertCheckbox(p notionProperty) string {
	if p.Checkbox == nil {
		return ""
	}
	if *p.Checkbox {
		return "true"
	}
	return "false"
}

func convertDate(p notionProperty) string {
	if p.Date == nil {
		return ""
	}
	if p.Date.End != "" {
		return p.Date.Start + " - " + p.Date.End
	}
	return p.Date.Start
}

func derefString(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

// richTextPlain extracts plain text from a slice of rich text elements.
func richTextPlain(texts []notionRichText) string {
	var sb []string
	for _, t := range texts {
		sb = append(sb, t.PlainText)
	}
	return strings.Join(sb, "")
}
