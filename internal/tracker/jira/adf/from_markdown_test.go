package adf

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Basic parsing ---

func TestFromMarkdown_empty(t *testing.T) {
	assert.Nil(t, FromMarkdown(""))
}

func TestFromMarkdown_whitespace_only(t *testing.T) {
	assert.Nil(t, FromMarkdown("   \n\n  "))
}

func TestFromMarkdown_plainText(t *testing.T) {
	doc := FromMarkdown("hello world")

	require.NotNil(t, doc)
	assert.Equal(t, 1, doc["version"])
	assert.Equal(t, "doc", doc["type"])

	content := docContent(t, doc)
	require.Len(t, content, 1)

	para := nodeAt(t, content, 0)
	assert.Equal(t, "paragraph", para["type"])

	paraContent := nodeContent(t, para)
	assert.Equal(t, "hello world", collectText(t, paraContent))
}

func TestFromMarkdown_multipleParagraphs(t *testing.T) {
	doc := FromMarkdown("first\n\nsecond")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 2)

	assert.Equal(t, "paragraph", nodeAt(t, content, 0)["type"])
	assert.Equal(t, "paragraph", nodeAt(t, content, 1)["type"])
}

func TestFromMarkdown_headings(t *testing.T) {
	tests := []struct {
		md    string
		level int
	}{
		{"# H1\n", 1},
		{"## H2\n", 2},
		{"### H3\n", 3},
		{"#### H4\n", 4},
		{"##### H5\n", 5},
		{"###### H6\n", 6},
	}

	for _, tt := range tests {
		doc := FromMarkdown(tt.md)
		require.NotNil(t, doc)

		content := docContent(t, doc)
		require.Len(t, content, 1)

		heading := nodeAt(t, content, 0)
		assert.Equal(t, "heading", heading["type"])

		attrs, ok := heading["attrs"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, tt.level, attrs["level"])
	}
}

func TestFromMarkdown_bold(t *testing.T) {
	doc := FromMarkdown("**bold**")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "bold", textNode["text"])
	assertHasMark(t, textNode, "strong")
}

func TestFromMarkdown_italic(t *testing.T) {
	doc := FromMarkdown("*italic*")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "italic", textNode["text"])
	assertHasMark(t, textNode, "em")
}

func TestFromMarkdown_boldItalic(t *testing.T) {
	doc := FromMarkdown("***bold italic***")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "bold italic", textNode["text"])
	assertHasMark(t, textNode, "strong")
	assertHasMark(t, textNode, "em")
}

func TestFromMarkdown_code(t *testing.T) {
	doc := FromMarkdown("`code`")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "code", textNode["text"])
	assertHasMark(t, textNode, "code")
}

func TestFromMarkdown_link(t *testing.T) {
	doc := FromMarkdown("[click](https://example.com)")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "click", textNode["text"])
	assertHasLinkMark(t, textNode, "https://example.com")
}

func TestFromMarkdown_bulletList(t *testing.T) {
	doc := FromMarkdown("- one\n- two\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 1)

	list := nodeAt(t, content, 0)
	assert.Equal(t, "bulletList", list["type"])

	items := nodeContent(t, list)
	require.Len(t, items, 2)

	item1 := nodeAt(t, items, 0)
	assert.Equal(t, "listItem", item1["type"])
}

func TestFromMarkdown_orderedList(t *testing.T) {
	doc := FromMarkdown("1. first\n2. second\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 1)

	list := nodeAt(t, content, 0)
	assert.Equal(t, "orderedList", list["type"])

	items := nodeContent(t, list)
	require.Len(t, items, 2)
}

func TestFromMarkdown_nestedList(t *testing.T) {
	doc := FromMarkdown("- outer\n  - inner\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	list := nodeAt(t, content, 0)
	assert.Equal(t, "bulletList", list["type"])

	items := nodeContent(t, list)
	require.Len(t, items, 1)

	// First item should have a nested list
	item := nodeAt(t, items, 0)
	itemContent := nodeContent(t, item)
	require.GreaterOrEqual(t, len(itemContent), 2)
	nested := nodeAt(t, itemContent, 1)
	assert.Equal(t, "bulletList", nested["type"])
}

func TestFromMarkdown_fencedCodeBlock(t *testing.T) {
	doc := FromMarkdown("```go\nfmt.Println()\n```\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 1)

	cb := nodeAt(t, content, 0)
	assert.Equal(t, "codeBlock", cb["type"])

	attrs, ok := cb["attrs"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "go", attrs["language"])

	cbContent := nodeContent(t, cb)
	require.Len(t, cbContent, 1)
	textNode := nodeAt(t, cbContent, 0)
	assert.Equal(t, "fmt.Println()\n", textNode["text"])
}

func TestFromMarkdown_fencedCodeBlock_no_language(t *testing.T) {
	doc := FromMarkdown("```\nsome code\n```\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	cb := nodeAt(t, content, 0)
	assert.Equal(t, "codeBlock", cb["type"])
	assert.Nil(t, cb["attrs"])
}

func TestFromMarkdown_indentedCodeBlock(t *testing.T) {
	doc := FromMarkdown("    indented code\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	cb := nodeAt(t, content, 0)
	assert.Equal(t, "codeBlock", cb["type"])
	assert.Nil(t, cb["attrs"])
}

func TestFromMarkdown_blockquote(t *testing.T) {
	doc := FromMarkdown("> quoted text\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 1)

	bq := nodeAt(t, content, 0)
	assert.Equal(t, "blockquote", bq["type"])
}

func TestFromMarkdown_nestedBlockquote(t *testing.T) {
	doc := FromMarkdown("> > nested\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	bq := nodeAt(t, content, 0)
	assert.Equal(t, "blockquote", bq["type"])

	bqContent := nodeContent(t, bq)
	innerBq := nodeAt(t, bqContent, 0)
	assert.Equal(t, "blockquote", innerBq["type"])
}

func TestFromMarkdown_horizontalRule(t *testing.T) {
	doc := FromMarkdown("---\n")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	require.Len(t, content, 1)

	rule := nodeAt(t, content, 0)
	assert.Equal(t, "rule", rule["type"])
}

// --- GFM extensions ---

func TestFromMarkdown_strikethrough(t *testing.T) {
	doc := FromMarkdown("~~deleted~~")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)
	textNode := nodeAt(t, paraContent, 0)

	assert.Equal(t, "deleted", textNode["text"])
	assertHasMark(t, textNode, "strike")
}

func TestFromMarkdown_table(t *testing.T) {
	md := "| Name | Age |\n|---|---|\n| Alice | 30 |\n"
	doc := FromMarkdown(md)
	require.NotNil(t, doc)

	content := docContent(t, doc)
	table := nodeAt(t, content, 0)
	assert.Equal(t, "table", table["type"])

	rows := nodeContent(t, table)
	require.Len(t, rows, 2)

	// First row is header
	headerRow := nodeAt(t, rows, 0)
	headerCells := nodeContent(t, headerRow)
	require.Len(t, headerCells, 2)
	assert.Equal(t, "tableHeader", nodeAt(t, headerCells, 0)["type"])

	// Second row is data
	dataRow := nodeAt(t, rows, 1)
	dataCells := nodeContent(t, dataRow)
	require.Len(t, dataCells, 2)
	assert.Equal(t, "tableCell", nodeAt(t, dataCells, 0)["type"])
}

func TestFromMarkdown_image(t *testing.T) {
	doc := FromMarkdown("![alt](https://example.com/img.png)")
	require.NotNil(t, doc)

	content := docContent(t, doc)
	para := nodeAt(t, content, 0)
	paraContent := nodeContent(t, para)

	card := nodeAt(t, paraContent, 0)
	assert.Equal(t, "inlineCard", card["type"])
	attrs, ok := card["attrs"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://example.com/img.png", attrs["url"])
}

// --- Round-trips (FromMarkdown → ToMarkdown) ---

func TestFromMarkdown_roundTrip(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want string
	}{
		{
			name: "plain paragraph",
			md:   "hello world",
			want: "hello world\n\n",
		},
		{
			name: "heading",
			md:   "# Title",
			want: "# Title\n\n",
		},
		{
			name: "sub-heading",
			md:   "### Sub-heading",
			want: "### Sub-heading\n\n",
		},
		{
			name: "bold",
			md:   "**bold**",
			want: "**bold**\n\n",
		},
		{
			name: "italic",
			md:   "*italic*",
			want: "*italic*\n\n",
		},
		{
			name: "inline code",
			md:   "`code`",
			want: "`code`\n\n",
		},
		{
			name: "bullet list",
			md:   "- one\n- two",
			want: "- one\n- two\n\n",
		},
		{
			name: "ordered list",
			md:   "1. first\n2. second",
			want: "1. first\n2. second\n\n",
		},
		{
			name: "code block",
			md:   "```go\nfmt.Println()\n```",
			want: "```go\nfmt.Println()\n```\n\n",
		},
		{
			name: "horizontal rule",
			md:   "---",
			want: "---\n\n",
		},
		{
			name: "blockquote",
			md:   "> quoted",
			want: "> quoted\n\n",
		},
		{
			name: "link",
			md:   "[click](https://example.com)",
			want: "[click](https://example.com)\n\n",
		},
		{
			name: "strikethrough",
			md:   "~~deleted~~",
			want: "~~deleted~~\n\n",
		},
		{
			name: "bold italic",
			md:   "***bold italic***",
			want: "***bold italic***\n\n",
		},
		{
			name: "mixed content",
			md:   "# Heading\n\nSome text\n\n- item1\n- item2",
			want: "# Heading\n\nSome text\n\n- item1\n- item2\n\n",
		},
		{
			name: "code block with markdown-like content",
			md:   "```\n**not bold** [not link](url)\n```",
			want: "```\n**not bold** [not link](url)\n```\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adfDoc := FromMarkdown(tt.md)
			require.NotNil(t, adfDoc)
			got := ToMarkdown(adfDoc)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Table round-trip ---

func TestFromMarkdown_roundTrip_table(t *testing.T) {
	md := "| Name | Age |\n|---|---|\n| Alice | 30 |\n"
	adfDoc := FromMarkdown(md)
	require.NotNil(t, adfDoc)

	got := ToMarkdown(adfDoc)
	assert.Contains(t, got, "| Name | Age |")
	assert.Contains(t, got, "|---|---|")
	assert.Contains(t, got, "| Alice | 30 |")
}

// --- Reverse round-trips (ADF → ToMarkdown → FromMarkdown → ToMarkdown) ---

func TestReverseRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		adf  map[string]any
	}{
		{
			name: "paragraph",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type": "paragraph",
						"content": []any{
							map[string]any{"type": "text", "text": "hello world"},
						},
					},
				},
			},
		},
		{
			name: "heading",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type":  "heading",
						"attrs": map[string]any{"level": float64(2)},
						"content": []any{
							map[string]any{"type": "text", "text": "Title"},
						},
					},
				},
			},
		},
		{
			name: "code block",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type":  "codeBlock",
						"attrs": map[string]any{"language": "go"},
						"content": []any{
							map[string]any{"type": "text", "text": "fmt.Println()\n"},
						},
					},
				},
			},
		},
		{
			name: "bullet list",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type": "bulletList",
						"content": []any{
							map[string]any{
								"type": "listItem",
								"content": []any{
									map[string]any{
										"type":    "paragraph",
										"content": []any{map[string]any{"type": "text", "text": "one"}},
									},
								},
							},
							map[string]any{
								"type": "listItem",
								"content": []any{
									map[string]any{
										"type":    "paragraph",
										"content": []any{map[string]any{"type": "text", "text": "two"}},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "blockquote",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type": "blockquote",
						"content": []any{
							map[string]any{
								"type": "paragraph",
								"content": []any{
									map[string]any{"type": "text", "text": "quoted"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "bold text",
			adf: map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type": "paragraph",
						"content": []any{
							map[string]any{
								"type": "text",
								"text": "bold",
								"marks": []any{
									map[string]any{"type": "strong"},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md1 := ToMarkdown(tt.adf)
			adf2 := FromMarkdown(md1)
			require.NotNil(t, adf2)
			md2 := ToMarkdown(adf2)
			assert.Equal(t, md1, md2)
		})
	}
}

// --- Test helpers ---

func docContent(t *testing.T, doc map[string]any) []any {
	t.Helper()
	content, ok := doc["content"].([]any)
	require.True(t, ok, "doc should have content array")
	return content
}

func nodeContent(t *testing.T, node map[string]any) []any {
	t.Helper()
	content, ok := node["content"].([]any)
	require.True(t, ok, "node %q should have content array", node["type"])
	return content
}

func nodeAt(t *testing.T, nodes []any, idx int) map[string]any {
	t.Helper()
	require.Greater(t, len(nodes), idx, "expected at least %d nodes", idx+1)
	node, ok := nodes[idx].(map[string]any)
	require.True(t, ok, "node at index %d should be map[string]any", idx)
	return node
}

func collectText(t *testing.T, nodes []any) string {
	t.Helper()
	var b strings.Builder
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "text", node["type"])
		b.WriteString(node["text"].(string))
	}
	return b.String()
}

func assertHasMark(t *testing.T, textNode map[string]any, markType string) {
	t.Helper()
	marks, ok := textNode["marks"].([]any)
	require.True(t, ok, "text node should have marks")
	found := false
	for _, m := range marks {
		mark, ok := m.(map[string]any)
		require.True(t, ok)
		if mark["type"] == markType {
			found = true
			break
		}
	}
	assert.True(t, found, "expected mark %q", markType)
}

func assertHasLinkMark(t *testing.T, textNode map[string]any, href string) {
	t.Helper()
	marks, ok := textNode["marks"].([]any)
	require.True(t, ok, "text node should have marks")
	found := false
	for _, m := range marks {
		mark, ok := m.(map[string]any)
		require.True(t, ok)
		if mark["type"] == "link" {
			attrs, ok := mark["attrs"].(map[string]any)
			require.True(t, ok)
			if attrs["href"] == href {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "expected link mark with href %q", href)
}
