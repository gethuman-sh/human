package figma

import (
	"fmt"
	"math"
)

const defaultMaxDepth = 3

// SummarizeNode converts a raw Figma node into a clean NodeSummary.
// maxDepth controls how deep to recurse into children (0 = no children).
func SummarizeNode(node figmaNode, maxDepth int) NodeSummary {
	ns := NodeSummary{
		ID:          node.ID,
		Name:        node.Name,
		Type:        node.Type,
		ComponentID: node.ComponentID,
	}

	if node.AbsoluteBoundingBox != nil {
		ns.Size = &Size{
			Width:  node.AbsoluteBoundingBox.Width,
			Height: node.AbsoluteBoundingBox.Height,
		}
	}

	if node.Characters != "" {
		ns.Text = node.Characters
	}

	if node.Style != nil && node.Style.FontFamily != "" {
		ns.Typography = &Typography{
			FontFamily: node.Style.FontFamily,
			FontSize:   node.Style.FontSize,
			FontWeight: node.Style.FontWeight,
		}
	}

	ns.Fills = FillsToStrings(node.Fills)

	if maxDepth > 0 {
		for _, child := range node.Children {
			ns.Children = append(ns.Children, SummarizeNode(child, maxDepth-1))
		}
	}

	return ns
}

// ColorToHex converts a Figma RGBA color to a hex string like "#FF5733".
func ColorToHex(c figmaColor) string {
	r := clampByte(c.R)
	g := clampByte(c.G)
	b := clampByte(c.B)
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}

// FillsToStrings converts a list of fills to human-readable color strings.
func FillsToStrings(fills []figmaPaint) []string {
	var result []string
	for _, fill := range fills {
		if fill.Visible != nil && !*fill.Visible {
			continue
		}
		if fill.Color != nil {
			result = append(result, ColorToHex(*fill.Color))
		}
	}
	return result
}

func clampByte(f float64) uint8 {
	return uint8(math.Round(math.Min(math.Max(f*255, 0), 255)))
}
