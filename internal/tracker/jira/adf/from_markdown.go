package adf

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// FromMarkdown parses markdown text and returns an ADF document.
// It returns nil for empty input.
func FromMarkdown(md string) map[string]any {
	if md == "" {
		return nil
	}

	src := []byte(md)
	gm := goldmark.New(goldmark.WithExtensions(extension.GFM))
	tree := gm.Parser().Parse(text.NewReader(src))

	content := convertChildren(tree, src)
	if len(content) == 0 {
		return nil
	}

	return map[string]any{
		"version": 1,
		"type":    "doc",
		"content": content,
	}
}

func convertChildren(n ast.Node, src []byte) []any {
	var nodes []any
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if converted := convertNode(child, src); converted != nil {
			nodes = append(nodes, converted)
		}
	}
	return nodes
}

func convertNode(n ast.Node, src []byte) map[string]any {
	switch n.Kind() {
	case ast.KindParagraph, ast.KindTextBlock:
		return blockWithContent("paragraph", nil, inlineChildren(n, src))

	case ast.KindHeading:
		heading := n.(*ast.Heading)
		return blockWithContent("heading",
			map[string]any{"level": heading.Level},
			inlineChildren(n, src))

	case ast.KindFencedCodeBlock:
		cb := n.(*ast.FencedCodeBlock)
		lang := ""
		if cb.Language(src) != nil {
			lang = string(cb.Language(src))
		}
		codeText := codeBlockText(cb, src)
		var attrs map[string]any
		if lang != "" {
			attrs = map[string]any{"language": lang}
		}
		content := []any{
			map[string]any{
				"type": "text",
				"text": codeText,
			},
		}
		return blockWithContent("codeBlock", attrs, content)

	case ast.KindCodeBlock:
		codeText := codeBlockText(n.(*ast.CodeBlock), src)
		content := []any{
			map[string]any{
				"type": "text",
				"text": codeText,
			},
		}
		return blockWithContent("codeBlock", nil, content)

	case ast.KindList:
		list := n.(*ast.List)
		nodeType := "bulletList"
		if list.IsOrdered() {
			nodeType = "orderedList"
		}
		return blockWithContent(nodeType, nil, convertChildren(n, src))

	case ast.KindListItem:
		return blockWithContent("listItem", nil, convertChildren(n, src))

	case ast.KindBlockquote:
		return blockWithContent("blockquote", nil, convertChildren(n, src))

	case ast.KindThematicBreak:
		return map[string]any{"type": "rule"}

	default:
		if n.Kind() == extast.KindTable {
			return convertTable(n, src)
		}
		return nil
	}
}

func inlineChildren(n ast.Node, src []byte) []any {
	var nodes []any
	collectInline(n, src, nil, &nodes)
	return nodes
}

func collectInline(n ast.Node, src []byte, marks []map[string]any, out *[]any) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.Kind() {
		case ast.KindText:
			tn := child.(*ast.Text)
			textNode := map[string]any{
				"type": "text",
				"text": string(tn.Value(src)),
			}
			if len(marks) > 0 {
				textNode["marks"] = copyMarks(marks)
			}
			*out = append(*out, textNode)
			if tn.SoftLineBreak() {
				*out = append(*out, map[string]any{"type": "hardBreak"})
			}
			if tn.HardLineBreak() {
				*out = append(*out, map[string]any{"type": "hardBreak"})
			}

		case ast.KindCodeSpan:
			codeMark := []map[string]any{{"type": "code"}}
			combined := appendMarks(marks, codeMark)
			collectInline(child, src, combined, out)

		case ast.KindEmphasis:
			em := child.(*ast.Emphasis)
			markType := "em"
			if em.Level == 2 {
				markType = "strong"
			}
			newMark := []map[string]any{{"type": markType}}
			combined := appendMarks(marks, newMark)
			collectInline(child, src, combined, out)

		case ast.KindLink:
			link := child.(*ast.Link)
			linkMark := []map[string]any{{
				"type": "link",
				"attrs": map[string]any{
					"href": string(link.Destination),
				},
			}}
			combined := appendMarks(marks, linkMark)
			collectInline(child, src, combined, out)

		case ast.KindAutoLink:
			al := child.(*ast.AutoLink)
			url := string(al.URL(src))
			label := string(al.Label(src))
			textNode := map[string]any{
				"type": "text",
				"text": label,
				"marks": []any{
					map[string]any{
						"type": "link",
						"attrs": map[string]any{
							"href": url,
						},
					},
				},
			}
			*out = append(*out, textNode)

		case ast.KindImage:
			img := child.(*ast.Image)
			*out = append(*out, map[string]any{
				"type": "inlineCard",
				"attrs": map[string]any{
					"url": string(img.Destination),
				},
			})

		default:
			if child.Kind() == extast.KindStrikethrough {
				strikeMark := []map[string]any{{"type": "strike"}}
				combined := appendMarks(marks, strikeMark)
				collectInline(child, src, combined, out)
			} else {
				collectInline(child, src, marks, out)
			}
		}
	}
}

type linesNode interface {
	Lines() *text.Segments
}

func codeBlockText(n linesNode, src []byte) string {
	var buf bytes.Buffer
	lines := n.Lines()
	for i := range lines.Len() {
		seg := lines.At(i)
		buf.Write(seg.Value(src))
	}
	return buf.String()
}

func blockWithContent(nodeType string, attrs map[string]any, content []any) map[string]any {
	node := map[string]any{"type": nodeType}
	if attrs != nil {
		node["attrs"] = attrs
	}
	if len(content) > 0 {
		node["content"] = content
	}
	return node
}

func copyMarks(marks []map[string]any) []any {
	result := make([]any, len(marks))
	for i, m := range marks {
		result[i] = m
	}
	return result
}

func appendMarks(existing, additional []map[string]any) []map[string]any {
	combined := make([]map[string]any, 0, len(existing)+len(additional))
	combined = append(combined, existing...)
	combined = append(combined, additional...)
	return combined
}

func convertTable(n ast.Node, src []byte) map[string]any {
	var rows []any
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		isHeader := child.Kind() == extast.KindTableHeader
		rows = append(rows, convertTableRow(child, src, isHeader))
	}
	return blockWithContent("table", nil, rows)
}

func convertTableRow(n ast.Node, src []byte, isHeader bool) map[string]any {
	var cells []any
	for cell := n.FirstChild(); cell != nil; cell = cell.NextSibling() {
		cellType := "tableCell"
		if isHeader {
			cellType = "tableHeader"
		}
		content := inlineChildren(cell, src)
		var para map[string]any
		if len(content) > 0 {
			para = blockWithContent("paragraph", nil, content)
		} else {
			para = map[string]any{"type": "paragraph"}
		}
		cells = append(cells, blockWithContent(cellType, nil, []any{para}))
	}
	return blockWithContent("tableRow", nil, cells)
}
