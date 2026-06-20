package adf

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type nodeRenderer func(map[string]any) string

var renderers map[string]nodeRenderer

func init() {
	renderers = map[string]nodeRenderer{
		"doc":          renderChildrenOnly,
		"listItem":     renderChildrenOnly,
		"mediaSingle":  renderChildrenOnly,
		"mediaGroup":   renderChildrenOnly,
		"paragraph":    renderParagraph,
		"heading":      renderHeading,
		"text":         renderText,
		"hardBreak":    renderHardBreak,
		"rule":         renderRule,
		"bulletList":   renderBulletList,
		"orderedList":  renderOrderedList,
		"codeBlock":    renderCodeBlock,
		"blockquote":   renderBlockquote,
		"inlineCard":   renderInlineCard,
		"mention":      renderMention,
		"emoji":        renderEmoji,
		"date":         renderDate,
		"status":       renderStatus,
		"panel":        renderPanel,
		"expand":       renderExpand,
		"nestedExpand": renderExpand,
		"table":        renderTable,
		"media":        renderMedia,
		"mediaInline":  renderMedia,
	}
}

func ToMarkdown(node map[string]any) string {
	nodeType, _ := node["type"].(string)
	if render, ok := renderers[nodeType]; ok {
		return render(node)
	}
	return renderChildren(node)
}

func renderChildrenOnly(node map[string]any) string {
	return renderChildren(node)
}

func renderParagraph(node map[string]any) string {
	return renderChildren(node) + "\n\n"
}

func renderHeading(node map[string]any) string {
	level := intAttr(node, "level", 1)
	prefix := strings.Repeat("#", level)
	return prefix + " " + renderChildren(node) + "\n\n"
}

func renderText(node map[string]any) string {
	text, _ := node["text"].(string)
	return applyMarks(text, node)
}

func renderHardBreak(_ map[string]any) string {
	return "\n"
}

func renderRule(_ map[string]any) string {
	return "---\n\n"
}

func renderBulletList(node map[string]any) string {
	return renderList(node, false)
}

func renderOrderedList(node map[string]any) string {
	return renderList(node, true)
}

func renderCodeBlock(node map[string]any) string {
	lang, _ := stringAttr(node, "language")
	body := renderChildren(node)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return "```" + lang + "\n" + body + "```\n\n"
}

func renderBlockquote(node map[string]any) string {
	inner := renderChildren(node)
	lines := strings.Split(strings.TrimRight(inner, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func renderInlineCard(node map[string]any) string {
	if u, ok := stringAttr(node, "url"); ok {
		return fmt.Sprintf("[%s](%s)", u, u)
	}
	return ""
}

func renderMention(node map[string]any) string {
	text, _ := stringAttr(node, "text")
	return text
}

func renderEmoji(node map[string]any) string {
	name, _ := stringAttr(node, "shortName")
	return name
}

func renderDate(node map[string]any) string {
	ts, _ := stringAttr(node, "timestamp")
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ts
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

func renderStatus(node map[string]any) string {
	text, _ := stringAttr(node, "text")
	return "[" + text + "]"
}

func renderExpand(node map[string]any) string {
	title, _ := stringAttr(node, "title")
	var b strings.Builder
	if title != "" {
		b.WriteString("**" + title + "**\n\n")
	}
	b.WriteString(renderChildren(node))
	return b.String()
}

func renderMedia(node map[string]any) string {
	if u, ok := stringAttr(node, "url"); ok {
		return fmt.Sprintf("[media](%s)", u)
	}
	return "[media]"
}

func renderChildren(node map[string]any) string {
	content, ok := node["content"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, child := range content {
		if childMap, ok := child.(map[string]any); ok {
			b.WriteString(ToMarkdown(childMap))
		}
	}
	return b.String()
}

func renderList(node map[string]any, ordered bool) string {
	content, ok := node["content"].([]any)
	if !ok {
		return ""
	}
	// Honour the ADF `attrs.order` start attribute so a list that
	// starts at 3 renders as "3." and not "1.", and only advance the
	// counter on actual list items so skipped children don't create
	// visual gaps in the numbering.
	start := 1
	if attrs, ok := node["attrs"].(map[string]any); ok {
		if v, ok := attrs["order"].(float64); ok && v > 0 {
			start = int(v)
		}
	}
	var b strings.Builder
	n := start
	for _, child := range content {
		childMap, ok := child.(map[string]any)
		if !ok {
			continue
		}
		inner := strings.TrimRight(renderChildren(childMap), "\n")
		if ordered {
			fmt.Fprintf(&b, "%d. %s\n", n, inner)
			n++
		} else {
			fmt.Fprintf(&b, "- %s\n", inner)
		}
	}
	b.WriteString("\n")
	return b.String()
}

func applyMarks(text string, node map[string]any) string {
	marks, ok := node["marks"].([]any)
	if !ok {
		return text
	}
	for _, m := range marks {
		mark, ok := m.(map[string]any)
		if !ok {
			continue
		}
		markType, _ := mark["type"].(string)
		switch markType {
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "*" + text + "*"
		case "code":
			text = "`" + text + "`"
		case "strike":
			text = "~~" + text + "~~"
		case "link":
			if href, ok := stringAttr(mark, "href"); ok {
				text = fmt.Sprintf("[%s](%s)", text, href)
			}
		}
	}
	return text
}

func stringAttr(node map[string]any, key string) (string, bool) {
	attrs, ok := node["attrs"].(map[string]any)
	if !ok {
		return "", false
	}
	val, ok := attrs[key].(string)
	return val, ok
}

func intAttr(node map[string]any, key string, fallback int) int {
	attrs, ok := node["attrs"].(map[string]any)
	if !ok {
		return fallback
	}
	switch v := attrs[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func renderPanel(node map[string]any) string {
	panelType, _ := stringAttr(node, "panelType")
	label := panelTypeLabel(panelType)
	inner := renderChildren(node)
	lines := strings.Split(strings.TrimRight(inner, "\n"), "\n")
	var b strings.Builder
	b.WriteString("> **" + label + "**\n")
	for _, line := range lines {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func panelTypeLabel(panelType string) string {
	switch panelType {
	case "info":
		return "Info:"
	case "note":
		return "Note:"
	case "warning":
		return "Warning:"
	case "error":
		return "Error:"
	case "success":
		return "Success:"
	default:
		return "Note:"
	}
}

func renderTable(node map[string]any) string {
	rows := nodeChildren(node)
	if len(rows) == 0 {
		return ""
	}

	var b strings.Builder
	firstRow := rows[0]
	firstCells := nodeChildren(firstRow)
	isHeader := len(firstCells) > 0 && nodeType(firstCells[0]) == "tableHeader"

	if isHeader {
		// First row is a real header row: render it, then the separator, then
		// the remaining rows as data.
		b.WriteString(renderTableRow(firstCells))
		b.WriteString("|")
		for range firstCells {
			b.WriteString("---|")
		}
		b.WriteString("\n")
		for _, row := range rows[1:] {
			cells := nodeChildren(row)
			b.WriteString(renderTableRow(cells))
		}
	} else {
		// No header row in the source. Emit a synthetic empty header row so
		// the resulting markdown is well-formed, then render every row from
		// the source as data — no row is silently promoted to a header.
		b.WriteString("|")
		for range firstCells {
			b.WriteString(" |")
		}
		b.WriteString("\n|")
		for range firstCells {
			b.WriteString("---|")
		}
		b.WriteString("\n")
		for _, row := range rows {
			cells := nodeChildren(row)
			b.WriteString(renderTableRow(cells))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderTableRow(cells []map[string]any) string {
	var b strings.Builder
	b.WriteString("|")
	for _, cell := range cells {
		b.WriteString(" ")
		b.WriteString(renderCellInline(cell))
		b.WriteString(" |")
	}
	b.WriteString("\n")
	return b.String()
}

func renderCellInline(cell map[string]any) string {
	inner := renderChildren(cell)
	// Strip trailing paragraph newlines for inline cell rendering
	return strings.TrimRight(inner, "\n")
}

func nodeChildren(node map[string]any) []map[string]any {
	content, ok := node["content"].([]any)
	if !ok {
		return nil
	}
	var result []map[string]any
	for _, child := range content {
		if childMap, ok := child.(map[string]any); ok {
			result = append(result, childMap)
		}
	}
	return result
}

func nodeType(node map[string]any) string {
	t, _ := node["type"].(string)
	return t
}
