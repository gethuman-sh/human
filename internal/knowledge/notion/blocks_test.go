package notion

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func rt(content string) []notionRichText {
	return []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: content},
		Annotations: notionAnnotations{},
		PlainText:   content,
	}}
}

func TestRenderBlock_paragraph(t *testing.T) {
	block := notionBlock{
		Type:      "paragraph",
		Paragraph: &textBlock{RichText: rt("Hello world")},
	}
	assert.Equal(t, "Hello world\n\n", renderBlock(block))
}

func TestRenderBlock_paragraph_empty(t *testing.T) {
	block := notionBlock{
		Type:      "paragraph",
		Paragraph: &textBlock{RichText: nil},
	}
	assert.Equal(t, "\n", renderBlock(block))
}

func TestRenderBlock_paragraph_nil(t *testing.T) {
	block := notionBlock{Type: "paragraph"}
	assert.Equal(t, "\n", renderBlock(block))
}

func TestRenderBlock_heading1(t *testing.T) {
	block := notionBlock{
		Type:     "heading_1",
		Heading1: &textBlock{RichText: rt("Title")},
	}
	assert.Equal(t, "# Title\n\n", renderBlock(block))
}

func TestRenderBlock_heading2(t *testing.T) {
	block := notionBlock{
		Type:     "heading_2",
		Heading2: &textBlock{RichText: rt("Subtitle")},
	}
	assert.Equal(t, "## Subtitle\n\n", renderBlock(block))
}

func TestRenderBlock_heading3(t *testing.T) {
	block := notionBlock{
		Type:     "heading_3",
		Heading3: &textBlock{RichText: rt("Section")},
	}
	assert.Equal(t, "### Section\n\n", renderBlock(block))
}

func TestRenderBlock_bulletedListItem(t *testing.T) {
	block := notionBlock{
		Type:             "bulleted_list_item",
		BulletedListItem: &textBlock{RichText: rt("Item one")},
	}
	assert.Equal(t, "- Item one\n", renderBlock(block))
}

func TestRenderBlock_numberedListItem(t *testing.T) {
	block := notionBlock{
		Type:             "numbered_list_item",
		NumberedListItem: &textBlock{RichText: rt("First")},
	}
	assert.Equal(t, "1. First\n", renderBlock(block))
}

func TestRenderBlock_toDo_checked(t *testing.T) {
	block := notionBlock{
		Type: "to_do",
		ToDo: &toDoBlock{RichText: rt("Done task"), Checked: true},
	}
	assert.Equal(t, "- [x] Done task\n", renderBlock(block))
}

func TestRenderBlock_toDo_unchecked(t *testing.T) {
	block := notionBlock{
		Type: "to_do",
		ToDo: &toDoBlock{RichText: rt("Pending task"), Checked: false},
	}
	assert.Equal(t, "- [ ] Pending task\n", renderBlock(block))
}

func TestRenderBlock_code(t *testing.T) {
	block := notionBlock{
		Type: "code",
		Code: &codeBlock{RichText: rt("fmt.Println()"), Language: "go"},
	}
	assert.Equal(t, "```go\nfmt.Println()\n```\n\n", renderBlock(block))
}

func TestRenderBlock_code_plainText(t *testing.T) {
	block := notionBlock{
		Type: "code",
		Code: &codeBlock{RichText: rt("hello"), Language: "plain text"},
	}
	assert.Equal(t, "```\nhello\n```\n\n", renderBlock(block))
}

func TestRenderBlock_quote(t *testing.T) {
	block := notionBlock{
		Type:  "quote",
		Quote: &textBlock{RichText: rt("A quote")},
	}
	assert.Equal(t, "> A quote\n\n", renderBlock(block))
}

func TestRenderBlock_divider(t *testing.T) {
	block := notionBlock{
		Type:    "divider",
		Divider: &struct{}{},
	}
	assert.Equal(t, "---\n\n", renderBlock(block))
}

func TestRenderBlock_callout_withEmoji(t *testing.T) {
	block := notionBlock{
		Type: "callout",
		Callout: &calloutBlock{
			RichText: rt("Important note"),
			Icon:     &notionIcon{Type: "emoji", Emoji: "💡"},
		},
	}
	assert.Equal(t, "> 💡 Important note\n\n", renderBlock(block))
}

func TestRenderBlock_callout_noIcon(t *testing.T) {
	block := notionBlock{
		Type: "callout",
		Callout: &calloutBlock{
			RichText: rt("Note"),
		},
	}
	assert.Equal(t, "> Note\n\n", renderBlock(block))
}

func TestRenderBlock_image_external(t *testing.T) {
	block := notionBlock{
		Type: "image",
		Image: &fileBlock{
			Type:     "external",
			External: &fileURL{URL: "https://example.com/img.png"},
			Caption:  rt("A screenshot"),
		},
	}
	assert.Equal(t, "![A screenshot](https://example.com/img.png)\n\n", renderBlock(block))
}

func TestRenderBlock_image_file(t *testing.T) {
	block := notionBlock{
		Type: "image",
		Image: &fileBlock{
			Type: "file",
			File: &fileURL{URL: "https://s3.aws/img.png"},
		},
	}
	assert.Equal(t, "![](https://s3.aws/img.png)\n\n", renderBlock(block))
}

func TestRenderBlock_bookmark(t *testing.T) {
	block := notionBlock{
		Type:     "bookmark",
		Bookmark: &bookmarkBlock{URL: "https://example.com"},
	}
	assert.Equal(t, "[https://example.com](https://example.com)\n\n", renderBlock(block))
}

func TestRenderBlock_childPage(t *testing.T) {
	block := notionBlock{
		Type:      "child_page",
		ChildPage: &childPageBlock{Title: "Sub Page"},
	}
	assert.Equal(t, "Child page: Sub Page\n\n", renderBlock(block))
}

func TestRenderBlock_unknown(t *testing.T) {
	block := notionBlock{Type: "unknown_type"}
	assert.Equal(t, "", renderBlock(block))
}

func TestRenderBlock_table(t *testing.T) {
	block := notionBlock{
		Type:  "table",
		Table: &tableBlock{TableWidth: 2, HasColumnHeader: true},
		Children: []notionBlock{
			{Type: "table_row", TableRow: &tableRowBlock{Cells: [][]notionRichText{rt("Name"), rt("Value")}}},
			{Type: "table_row", TableRow: &tableRowBlock{Cells: [][]notionRichText{rt("A"), rt("1")}}},
			{Type: "table_row", TableRow: &tableRowBlock{Cells: [][]notionRichText{rt("B"), rt("2")}}},
		},
	}
	expected := "| Name | Value |\n| --- | --- |\n| A | 1 |\n| B | 2 |\n\n"
	assert.Equal(t, expected, renderBlock(block))
}

func TestRenderBlock_table_empty(t *testing.T) {
	block := notionBlock{
		Type:  "table",
		Table: &tableBlock{TableWidth: 2},
	}
	assert.Equal(t, "", renderBlock(block))
}

// TestRenderBlock_table_noColumnHeader asserts that when the source table
// has HasColumnHeader=false, no source row is silently promoted to a
// header — the output instead includes a synthetic empty header row and
// every source row appears as data.
func TestRenderBlock_table_noColumnHeader(t *testing.T) {
	block := notionBlock{
		Type:  "table",
		Table: &tableBlock{TableWidth: 2, HasColumnHeader: false},
		Children: []notionBlock{
			{Type: "table_row", TableRow: &tableRowBlock{Cells: [][]notionRichText{rt("A"), rt("1")}}},
			{Type: "table_row", TableRow: &tableRowBlock{Cells: [][]notionRichText{rt("B"), rt("2")}}},
		},
	}
	out := renderBlock(block)
	// First non-empty line is the synthetic empty header.
	assert.Contains(t, out, "| A | 1 |")
	assert.Contains(t, out, "| B | 2 |")
	// Both source rows must appear as data — neither should be missing.
	assert.Equal(t, 1, strings.Count(out, "| A | 1 |"))
	assert.Equal(t, 1, strings.Count(out, "| B | 2 |"))
}

func TestRichTextToMarkdown_bold(t *testing.T) {
	texts := []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: "bold"},
		Annotations: notionAnnotations{Bold: true},
	}}
	assert.Equal(t, "**bold**", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_italic(t *testing.T) {
	texts := []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: "italic"},
		Annotations: notionAnnotations{Italic: true},
	}}
	assert.Equal(t, "*italic*", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_strikethrough(t *testing.T) {
	texts := []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: "strike"},
		Annotations: notionAnnotations{Strikethrough: true},
	}}
	assert.Equal(t, "~~strike~~", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_code(t *testing.T) {
	texts := []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: "code"},
		Annotations: notionAnnotations{Code: true},
	}}
	assert.Equal(t, "`code`", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_link(t *testing.T) {
	href := "https://example.com"
	texts := []notionRichText{{
		Type: "text",
		Text: notionTextObj{Content: "click here"},
		Href: &href,
	}}
	assert.Equal(t, "[click here](https://example.com)", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_stacked(t *testing.T) {
	href := "https://example.com"
	texts := []notionRichText{{
		Type:        "text",
		Text:        notionTextObj{Content: "important"},
		Annotations: notionAnnotations{Bold: true, Italic: true},
		Href:        &href,
	}}
	assert.Equal(t, "[***important***](https://example.com)", richTextToMarkdown(texts))
}

func TestRichTextToMarkdown_multiple(t *testing.T) {
	texts := []notionRichText{
		{Type: "text", Text: notionTextObj{Content: "Hello "}, Annotations: notionAnnotations{}},
		{Type: "text", Text: notionTextObj{Content: "world"}, Annotations: notionAnnotations{Bold: true}},
	}
	assert.Equal(t, "Hello **world**", richTextToMarkdown(texts))
}

func TestBlocksToMarkdown(t *testing.T) {
	blocks := []notionBlock{
		{Type: "heading_1", Heading1: &textBlock{RichText: rt("Title")}},
		{Type: "paragraph", Paragraph: &textBlock{RichText: rt("Some text")}},
		{Type: "bulleted_list_item", BulletedListItem: &textBlock{RichText: rt("Item")}},
	}
	expected := "# Title\n\nSome text\n\n- Item\n"
	assert.Equal(t, expected, BlocksToMarkdown(blocks))
}

func TestRenderBlock_bulletedListItem_withChildren(t *testing.T) {
	block := notionBlock{
		Type:             "bulleted_list_item",
		BulletedListItem: &textBlock{RichText: rt("Parent")},
		Children: []notionBlock{
			{Type: "paragraph", Paragraph: &textBlock{RichText: rt("Child")}},
		},
	}
	expected := "- Parent\n  Child\n\n"
	assert.Equal(t, expected, renderBlock(block))
}

func TestRenderBlock_numberedListItem_withChildren(t *testing.T) {
	block := notionBlock{
		Type:             "numbered_list_item",
		NumberedListItem: &textBlock{RichText: rt("Parent")},
		Children: []notionBlock{
			{Type: "paragraph", Paragraph: &textBlock{RichText: rt("Child")}},
		},
	}
	expected := "1. Parent\n   Child\n\n"
	assert.Equal(t, expected, renderBlock(block))
}
