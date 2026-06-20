package adf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_stringAttr(t *testing.T) {
	tests := []struct {
		name   string
		node   map[string]any
		key    string
		want   string
		wantOK bool
	}{
		{
			name:   "returns string attribute",
			node:   map[string]any{"attrs": map[string]any{"language": "go"}},
			key:    "language",
			want:   "go",
			wantOK: true,
		},
		{
			name:   "missing attrs",
			node:   map[string]any{},
			key:    "language",
			want:   "",
			wantOK: false,
		},
		{
			name:   "attrs is wrong type",
			node:   map[string]any{"attrs": "not-a-map"},
			key:    "language",
			want:   "",
			wantOK: false,
		},
		{
			name:   "key missing from attrs",
			node:   map[string]any{"attrs": map[string]any{}},
			key:    "language",
			want:   "",
			wantOK: false,
		},
		{
			name:   "value is not a string",
			node:   map[string]any{"attrs": map[string]any{"language": 42}},
			key:    "language",
			want:   "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stringAttr(tt.node, tt.key)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func Test_intAttr(t *testing.T) {
	tests := []struct {
		name     string
		node     map[string]any
		key      string
		fallback int
		want     int
	}{
		{
			name:     "returns int from float64",
			node:     map[string]any{"attrs": map[string]any{"level": float64(3)}},
			key:      "level",
			fallback: 1,
			want:     3,
		},
		{
			name:     "missing attrs returns fallback",
			node:     map[string]any{},
			key:      "level",
			fallback: 1,
			want:     1,
		},
		{
			name:     "attrs is wrong type returns fallback",
			node:     map[string]any{"attrs": "bad"},
			key:      "level",
			fallback: 1,
			want:     1,
		},
		{
			name:     "key missing returns fallback",
			node:     map[string]any{"attrs": map[string]any{}},
			key:      "level",
			fallback: 5,
			want:     5,
		},
		{
			name:     "value is not float64 returns fallback",
			node:     map[string]any{"attrs": map[string]any{"level": "three"}},
			key:      "level",
			fallback: 1,
			want:     1,
		},
		{
			name:     "zero value is not fallback",
			node:     map[string]any{"attrs": map[string]any{"level": float64(0)}},
			key:      "level",
			fallback: 1,
			want:     0,
		},
		{
			name:     "returns int from int",
			node:     map[string]any{"attrs": map[string]any{"level": 3}},
			key:      "level",
			fallback: 1,
			want:     3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intAttr(tt.node, tt.key, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Block nodes ---

func TestToMarkdown_doc_multiple_children(t *testing.T) {
	node := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "first"},
				},
			},
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "second"},
				},
			},
		},
	}
	assert.Equal(t, "first\n\nsecond\n\n", ToMarkdown(node))
}

func TestToMarkdown_paragraph_multiple_text(t *testing.T) {
	node := map[string]any{
		"type": "paragraph",
		"content": []any{
			map[string]any{"type": "text", "text": "hello "},
			map[string]any{"type": "text", "text": "world"},
		},
	}
	assert.Equal(t, "hello world\n\n", ToMarkdown(node))
}

func TestToMarkdown_paragraph_empty(t *testing.T) {
	node := map[string]any{
		"type": "paragraph",
	}
	assert.Equal(t, "\n\n", ToMarkdown(node))
}

func TestToMarkdown_heading(t *testing.T) {
	node := map[string]any{
		"type":  "heading",
		"attrs": map[string]any{"level": float64(2)},
		"content": []any{
			map[string]any{"type": "text", "text": "Title"},
		},
	}
	assert.Equal(t, "## Title\n\n", ToMarkdown(node))
}

func TestToMarkdown_heading_default_level(t *testing.T) {
	node := map[string]any{
		"type": "heading",
		"content": []any{
			map[string]any{"type": "text", "text": "Title"},
		},
	}
	assert.Equal(t, "# Title\n\n", ToMarkdown(node))
}

func TestToMarkdown_hardBreak(t *testing.T) {
	node := map[string]any{"type": "hardBreak"}
	assert.Equal(t, "\n", ToMarkdown(node))
}

func TestToMarkdown_rule(t *testing.T) {
	node := map[string]any{"type": "rule"}
	assert.Equal(t, "---\n\n", ToMarkdown(node))
}

func TestToMarkdown_bulletList(t *testing.T) {
	node := map[string]any{
		"type": "bulletList",
		"content": []any{
			listItem("one"),
			listItem("two"),
			listItem("three"),
		},
	}
	assert.Equal(t, "- one\n- two\n- three\n\n", ToMarkdown(node))
}

func TestToMarkdown_orderedList(t *testing.T) {
	node := map[string]any{
		"type": "orderedList",
		"content": []any{
			listItem("first"),
			listItem("second"),
			listItem("third"),
		},
	}
	assert.Equal(t, "1. first\n2. second\n3. third\n\n", ToMarkdown(node))
}

func TestToMarkdown_blockquote(t *testing.T) {
	node := map[string]any{
		"type": "blockquote",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "quoted text"},
				},
			},
		},
	}
	assert.Equal(t, "> quoted text\n\n", ToMarkdown(node))
}

func TestToMarkdown_blockquote_multiline(t *testing.T) {
	node := map[string]any{
		"type": "blockquote",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "line one"},
					map[string]any{"type": "hardBreak"},
					map[string]any{"type": "text", "text": "line two"},
				},
			},
		},
	}
	assert.Equal(t, "> line one\n> line two\n\n", ToMarkdown(node))
}

func TestToMarkdown_codeBlock(t *testing.T) {
	node := map[string]any{
		"type":  "codeBlock",
		"attrs": map[string]any{"language": "go"},
		"content": []any{
			map[string]any{"type": "text", "text": "fmt.Println()"},
		},
	}
	assert.Equal(t, "```go\nfmt.Println()\n```\n\n", ToMarkdown(node))
}

func TestToMarkdown_codeBlock_no_language(t *testing.T) {
	node := map[string]any{
		"type": "codeBlock",
		"content": []any{
			map[string]any{"type": "text", "text": "code"},
		},
	}
	assert.Equal(t, "```\ncode\n```\n\n", ToMarkdown(node))
}

func TestToMarkdown_codeBlock_trailing_newline(t *testing.T) {
	node := map[string]any{
		"type": "codeBlock",
		"content": []any{
			map[string]any{"type": "text", "text": "code\n"},
		},
	}
	// Should not double the newline before fence
	assert.Equal(t, "```\ncode\n```\n\n", ToMarkdown(node))
}

func TestToMarkdown_codeBlock_no_trailing_newline(t *testing.T) {
	node := map[string]any{
		"type": "codeBlock",
		"content": []any{
			map[string]any{"type": "text", "text": "code"},
		},
	}
	// Should insert newline before closing fence
	assert.Equal(t, "```\ncode\n```\n\n", ToMarkdown(node))
}

func TestToMarkdown_nestedList(t *testing.T) {
	node := map[string]any{
		"type": "bulletList",
		"content": []any{
			map[string]any{
				"type": "listItem",
				"content": []any{
					map[string]any{
						"type":    "paragraph",
						"content": []any{map[string]any{"type": "text", "text": "outer"}},
					},
					map[string]any{
						"type": "bulletList",
						"content": []any{
							listItem("inner"),
						},
					},
				},
			},
		},
	}
	got := ToMarkdown(node)
	assert.Contains(t, got, "outer")
	assert.Contains(t, got, "inner")
}

func TestToMarkdown_unknownNodeType(t *testing.T) {
	node := map[string]any{
		"type": "unknownWidget",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "fallback content"},
				},
			},
		},
	}
	assert.Equal(t, "fallback content\n\n", ToMarkdown(node))
}

func TestToMarkdown_emptyDoc(t *testing.T) {
	node := map[string]any{
		"type": "doc",
	}
	assert.Equal(t, "", ToMarkdown(node))
}

// --- Inline nodes ---

func TestToMarkdown_inlineCard(t *testing.T) {
	node := map[string]any{
		"type":  "inlineCard",
		"attrs": map[string]any{"url": "https://example.com"},
	}
	assert.Equal(t, "[https://example.com](https://example.com)", ToMarkdown(node))
}

func TestToMarkdown_inlineCard_no_url(t *testing.T) {
	node := map[string]any{
		"type": "inlineCard",
	}
	assert.Equal(t, "", ToMarkdown(node))
}

func TestToMarkdown_mention(t *testing.T) {
	node := map[string]any{
		"type":  "mention",
		"attrs": map[string]any{"text": "@John", "id": "123"},
	}
	assert.Equal(t, "@John", ToMarkdown(node))
}

func TestToMarkdown_mention_no_text(t *testing.T) {
	node := map[string]any{
		"type":  "mention",
		"attrs": map[string]any{"id": "123"},
	}
	assert.Equal(t, "", ToMarkdown(node))
}

func TestToMarkdown_emoji(t *testing.T) {
	node := map[string]any{
		"type":  "emoji",
		"attrs": map[string]any{"shortName": ":thumbsup:"},
	}
	assert.Equal(t, ":thumbsup:", ToMarkdown(node))
}

func TestToMarkdown_date(t *testing.T) {
	// 1700000000000 ms = 2023-11-14T22:13:20Z
	node := map[string]any{
		"type":  "date",
		"attrs": map[string]any{"timestamp": "1700000000000"},
	}
	assert.Equal(t, "2023-11-14", ToMarkdown(node))
}

func TestToMarkdown_date_invalid(t *testing.T) {
	node := map[string]any{
		"type":  "date",
		"attrs": map[string]any{"timestamp": "not-a-number"},
	}
	assert.Equal(t, "not-a-number", ToMarkdown(node))
}

func TestToMarkdown_status(t *testing.T) {
	node := map[string]any{
		"type":  "status",
		"attrs": map[string]any{"text": "IN PROGRESS", "color": "blue"},
	}
	assert.Equal(t, "[IN PROGRESS]", ToMarkdown(node))
}

func TestToMarkdown_media_with_url(t *testing.T) {
	node := map[string]any{
		"type":  "media",
		"attrs": map[string]any{"url": "https://example.com/img.png", "type": "external"},
	}
	assert.Equal(t, "[media](https://example.com/img.png)", ToMarkdown(node))
}

func TestToMarkdown_media_no_url(t *testing.T) {
	node := map[string]any{
		"type":  "media",
		"attrs": map[string]any{"id": "abc-123", "type": "file"},
	}
	assert.Equal(t, "[media]", ToMarkdown(node))
}

func TestToMarkdown_mediaInline(t *testing.T) {
	node := map[string]any{
		"type":  "mediaInline",
		"attrs": map[string]any{"url": "https://example.com/doc.pdf"},
	}
	assert.Equal(t, "[media](https://example.com/doc.pdf)", ToMarkdown(node))
}

func TestToMarkdown_mediaSingle(t *testing.T) {
	node := map[string]any{
		"type": "mediaSingle",
		"content": []any{
			map[string]any{
				"type":  "media",
				"attrs": map[string]any{"url": "https://example.com/img.png"},
			},
		},
	}
	assert.Equal(t, "[media](https://example.com/img.png)", ToMarkdown(node))
}

// --- Marks ---

func TestToMarkdown_link_mark(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "click here",
		"marks": []any{
			map[string]any{
				"type":  "link",
				"attrs": map[string]any{"href": "https://example.com"},
			},
		},
	}
	assert.Equal(t, "[click here](https://example.com)", ToMarkdown(node))
}

func TestToMarkdown_link_mark_no_href(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "click here",
		"marks": []any{
			map[string]any{
				"type": "link",
			},
		},
	}
	assert.Equal(t, "click here", ToMarkdown(node))
}

func TestToMarkdown_strike_mark(t *testing.T) {
	node := textWithMark("deleted", "strike")
	assert.Equal(t, "~~deleted~~", ToMarkdown(node))
}

func TestToMarkdown_underline_mark(t *testing.T) {
	node := textWithMark("underlined", "underline")
	assert.Equal(t, "underlined", ToMarkdown(node))
}

func TestToMarkdown_subsup_mark(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "subscript",
		"marks": []any{
			map[string]any{"type": "subsup", "attrs": map[string]any{"type": "sub"}},
		},
	}
	assert.Equal(t, "subscript", ToMarkdown(node))
}

func TestToMarkdown_textColor_mark(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "colored",
		"marks": []any{
			map[string]any{"type": "textColor", "attrs": map[string]any{"color": "#ff0000"}},
		},
	}
	assert.Equal(t, "colored", ToMarkdown(node))
}

func TestToMarkdown_combined_strong_em(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "both",
		"marks": []any{
			map[string]any{"type": "strong"},
			map[string]any{"type": "em"},
		},
	}
	assert.Equal(t, "***both***", ToMarkdown(node))
}

func TestToMarkdown_combined_link_strong(t *testing.T) {
	node := map[string]any{
		"type": "text",
		"text": "bold link",
		"marks": []any{
			map[string]any{"type": "strong"},
			map[string]any{
				"type":  "link",
				"attrs": map[string]any{"href": "https://example.com"},
			},
		},
	}
	assert.Equal(t, "[**bold link**](https://example.com)", ToMarkdown(node))
}

func TestToMarkdown_unknown_mark(t *testing.T) {
	node := textWithMark("text", "unknownMark")
	assert.Equal(t, "text", ToMarkdown(node))
}

// --- Panel ---

func TestToMarkdown_panel_info(t *testing.T) {
	node := map[string]any{
		"type":  "panel",
		"attrs": map[string]any{"panelType": "info"},
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "important info"}},
			},
		},
	}
	assert.Equal(t, "> **Info:**\n> important info\n\n", ToMarkdown(node))
}

func TestToMarkdown_panel_warning(t *testing.T) {
	node := map[string]any{
		"type":  "panel",
		"attrs": map[string]any{"panelType": "warning"},
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "be careful"}},
			},
		},
	}
	assert.Equal(t, "> **Warning:**\n> be careful\n\n", ToMarkdown(node))
}

func TestToMarkdown_panel_no_type(t *testing.T) {
	node := map[string]any{
		"type": "panel",
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "note text"}},
			},
		},
	}
	assert.Equal(t, "> **Note:**\n> note text\n\n", ToMarkdown(node))
}

// --- Table ---

func TestToMarkdown_table_with_header(t *testing.T) {
	node := map[string]any{
		"type": "table",
		"content": []any{
			tableRow("tableHeader", "Name", "Age"),
			tableRow("tableCell", "Alice", "30"),
			tableRow("tableCell", "Bob", "25"),
		},
	}
	expected := "| Name | Age |\n|---|---|\n| Alice | 30 |\n| Bob | 25 |\n\n"
	assert.Equal(t, expected, ToMarkdown(node))
}

func TestToMarkdown_table_header_only(t *testing.T) {
	node := map[string]any{
		"type": "table",
		"content": []any{
			tableRow("tableHeader", "Col1", "Col2"),
		},
	}
	expected := "| Col1 | Col2 |\n|---|---|\n\n"
	assert.Equal(t, expected, ToMarkdown(node))
}

func TestToMarkdown_table_no_header(t *testing.T) {
	node := map[string]any{
		"type": "table",
		"content": []any{
			tableRow("tableCell", "a", "b"),
			tableRow("tableCell", "c", "d"),
		},
	}
	// No header in source → synthetic empty header, then both rows as data.
	// Neither source row should be silently promoted to a header.
	expected := "| | |\n|---|---|\n| a | b |\n| c | d |\n\n"
	assert.Equal(t, expected, ToMarkdown(node))
}

func TestToMarkdown_table_cell_with_bold(t *testing.T) {
	node := map[string]any{
		"type": "table",
		"content": []any{
			map[string]any{
				"type": "tableRow",
				"content": []any{
					map[string]any{
						"type": "tableHeader",
						"content": []any{
							map[string]any{
								"type": "paragraph",
								"content": []any{
									map[string]any{
										"type": "text", "text": "bold",
										"marks": []any{map[string]any{"type": "strong"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	got := ToMarkdown(node)
	assert.Contains(t, got, "**bold**")
}

// --- Expand ---

func TestToMarkdown_expand_with_title(t *testing.T) {
	node := map[string]any{
		"type":  "expand",
		"attrs": map[string]any{"title": "Details"},
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "hidden content"}},
			},
		},
	}
	assert.Equal(t, "**Details**\n\nhidden content\n\n", ToMarkdown(node))
}

func TestToMarkdown_expand_no_title(t *testing.T) {
	node := map[string]any{
		"type": "expand",
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "content"}},
			},
		},
	}
	assert.Equal(t, "content\n\n", ToMarkdown(node))
}

func TestToMarkdown_nestedExpand(t *testing.T) {
	node := map[string]any{
		"type":  "nestedExpand",
		"attrs": map[string]any{"title": "Nested"},
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "nested content"}},
			},
		},
	}
	assert.Equal(t, "**Nested**\n\nnested content\n\n", ToMarkdown(node))
}

// --- Edge cases ---

func TestToMarkdown_deeply_nested(t *testing.T) {
	node := map[string]any{
		"type": "blockquote",
		"content": []any{
			map[string]any{
				"type": "bulletList",
				"content": []any{
					map[string]any{
						"type": "listItem",
						"content": []any{
							map[string]any{
								"type": "paragraph",
								"content": []any{
									map[string]any{
										"type": "text", "text": "bold item",
										"marks": []any{map[string]any{"type": "strong"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	got := ToMarkdown(node)
	assert.Contains(t, got, "**bold item**")
	assert.Contains(t, got, "> ")
}

// --- Test helpers ---

func textWithMark(text, markType string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": text,
		"marks": []any{
			map[string]any{"type": markType},
		},
	}
}

func listItem(text string) map[string]any {
	return map[string]any{
		"type": "listItem",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	}
}

func tableRow(cellType string, cells ...string) map[string]any {
	var content []any
	for _, text := range cells {
		content = append(content, map[string]any{
			"type": cellType,
			"content": []any{
				map[string]any{
					"type": "paragraph",
					"content": []any{
						map[string]any{"type": "text", "text": text},
					},
				},
			},
		})
	}
	return map[string]any{
		"type":    "tableRow",
		"content": content,
	}
}
