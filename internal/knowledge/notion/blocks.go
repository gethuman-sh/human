package notion

import (
	"fmt"
	"strings"
)

// BlocksToMarkdown converts a slice of Notion blocks to a markdown string.
func BlocksToMarkdown(blocks []notionBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(renderBlock(b))
	}
	return sb.String()
}

// headingPrefixes maps heading block types to their markdown prefixes.
var headingPrefixes = map[string]string{
	"heading_1": "# ",
	"heading_2": "## ",
	"heading_3": "### ",
}

func renderBlock(block notionBlock) string {
	if prefix, ok := headingPrefixes[block.Type]; ok {
		return renderHeading(block.headingBlock(), prefix)
	}
	switch block.Type {
	case "paragraph":
		return renderParagraph(block)
	case "bulleted_list_item":
		return renderListItem(block.BulletedListItem, "- ", "  ", block.Children)
	case "numbered_list_item":
		return renderListItem(block.NumberedListItem, "1. ", "   ", block.Children)
	case "to_do":
		return renderToDo(block)
	case "code":
		return renderCode(block)
	case "quote":
		return renderQuote(block)
	case "divider":
		return "---\n\n"
	case "callout":
		return renderCallout(block)
	case "image":
		return renderImage(block)
	case "bookmark":
		return renderBookmark(block)
	case "child_page":
		return renderChildPage(block)
	case "table":
		return renderTable(block)
	default:
		return ""
	}
}

func renderParagraph(block notionBlock) string {
	if block.Paragraph == nil {
		return "\n"
	}
	text := richTextToMarkdown(block.Paragraph.RichText)
	if text == "" {
		return "\n"
	}
	return text + "\n\n"
}

func renderHeading(tb *textBlock, prefix string) string {
	if tb == nil {
		return ""
	}
	return prefix + richTextToMarkdown(tb.RichText) + "\n\n"
}

func renderListItem(tb *textBlock, prefix, indent string, children []notionBlock) string {
	if tb == nil {
		return ""
	}
	result := prefix + richTextToMarkdown(tb.RichText) + "\n"
	for _, child := range children {
		result += indent + renderBlock(child)
	}
	return result
}

func renderToDo(block notionBlock) string {
	if block.ToDo == nil {
		return ""
	}
	checkbox := "- [ ] "
	if block.ToDo.Checked {
		checkbox = "- [x] "
	}
	return checkbox + richTextToMarkdown(block.ToDo.RichText) + "\n"
}

func renderCode(block notionBlock) string {
	if block.Code == nil {
		return ""
	}
	lang := block.Code.Language
	if lang == "plain text" {
		lang = ""
	}
	return "```" + lang + "\n" + richTextToMarkdown(block.Code.RichText) + "\n```\n\n"
}

func renderQuote(block notionBlock) string {
	if block.Quote == nil {
		return ""
	}
	return "> " + richTextToMarkdown(block.Quote.RichText) + "\n\n"
}

func renderCallout(block notionBlock) string {
	if block.Callout == nil {
		return ""
	}
	emoji := ""
	if block.Callout.Icon != nil && block.Callout.Icon.Emoji != "" {
		emoji = block.Callout.Icon.Emoji + " "
	}
	return "> " + emoji + richTextToMarkdown(block.Callout.RichText) + "\n\n"
}

func renderImage(block notionBlock) string {
	if block.Image == nil {
		return ""
	}
	url := imageURL(block.Image)
	caption := richTextToMarkdown(block.Image.Caption)
	return fmt.Sprintf("![%s](%s)\n\n", caption, url)
}

func renderBookmark(block notionBlock) string {
	if block.Bookmark == nil {
		return ""
	}
	url := block.Bookmark.URL
	return fmt.Sprintf("[%s](%s)\n\n", url, url)
}

func renderChildPage(block notionBlock) string {
	if block.ChildPage == nil {
		return ""
	}
	return fmt.Sprintf("Child page: %s\n\n", block.ChildPage.Title)
}

func renderTable(block notionBlock) string {
	if len(block.Children) == 0 {
		return ""
	}

	hasColumnHeader := block.Table != nil && block.Table.HasColumnHeader

	// Pre-render every row so we can emit a synthetic empty header when the
	// source table has no header of its own.
	var rendered []string
	var cellCount int
	for _, child := range block.Children {
		if child.TableRow == nil {
			continue
		}
		cells := make([]string, 0, len(child.TableRow.Cells))
		for _, cell := range child.TableRow.Cells {
			cells = append(cells, richTextToMarkdown(cell))
		}
		if cellCount == 0 {
			cellCount = len(cells)
		}
		rendered = append(rendered, "| "+strings.Join(cells, " | ")+" |\n")
	}
	if len(rendered) == 0 || cellCount == 0 {
		return ""
	}

	separator := "|" + strings.Repeat(" --- |", cellCount) + "\n"

	var sb strings.Builder
	if hasColumnHeader {
		sb.WriteString(rendered[0])
		sb.WriteString(separator)
		for _, line := range rendered[1:] {
			sb.WriteString(line)
		}
	} else {
		// Synthesize an empty header row so the markdown table is well-formed
		// and no source row is silently promoted to a header.
		sb.WriteString("|" + strings.Repeat("  |", cellCount) + "\n")
		sb.WriteString(separator)
		for _, line := range rendered {
			sb.WriteString(line)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

func imageURL(img *fileBlock) string {
	if img.External != nil {
		return img.External.URL
	}
	if img.File != nil {
		return img.File.URL
	}
	return ""
}

// richTextToMarkdown converts a slice of rich text elements to markdown.
func richTextToMarkdown(texts []notionRichText) string {
	var sb strings.Builder
	for _, t := range texts {
		sb.WriteString(renderRichText(t))
	}
	return sb.String()
}

func renderRichText(t notionRichText) string {
	text := t.Text.Content
	if t.Annotations.Code {
		text = "`" + text + "`"
	}
	if t.Annotations.Bold {
		text = "**" + text + "**"
	}
	if t.Annotations.Italic {
		text = "*" + text + "*"
	}
	if t.Annotations.Strikethrough {
		text = "~~" + text + "~~"
	}
	if t.Href != nil && *t.Href != "" {
		text = "[" + text + "](" + *t.Href + ")"
	}
	return text
}
