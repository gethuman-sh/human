package figma

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeNode_basic(t *testing.T) {
	node := figmaNode{
		ID:   "0:1",
		Name: "Header",
		Type: "FRAME",
		AbsoluteBoundingBox: &figmaBoundingBox{
			X: 0, Y: 0, Width: 1440, Height: 80,
		},
	}

	ns := SummarizeNode(node, 0)
	assert.Equal(t, "0:1", ns.ID)
	assert.Equal(t, "Header", ns.Name)
	assert.Equal(t, "FRAME", ns.Type)
	assert.NotNil(t, ns.Size)
	assert.Equal(t, 1440.0, ns.Size.Width)
	assert.Equal(t, 80.0, ns.Size.Height)
}

func TestSummarizeNode_textNode(t *testing.T) {
	node := figmaNode{
		ID:         "1:2",
		Name:       "Title",
		Type:       "TEXT",
		Characters: "Welcome to Figma",
		Style: &figmaTextStyle{
			FontFamily: "Inter",
			FontSize:   24,
			FontWeight: 700,
		},
	}

	ns := SummarizeNode(node, 0)
	assert.Equal(t, "Welcome to Figma", ns.Text)
	assert.NotNil(t, ns.Typography)
	assert.Equal(t, "Inter", ns.Typography.FontFamily)
	assert.Equal(t, 24.0, ns.Typography.FontSize)
	assert.Equal(t, 700.0, ns.Typography.FontWeight)
}

func TestSummarizeNode_withFills(t *testing.T) {
	node := figmaNode{
		ID:   "2:3",
		Name: "Box",
		Type: "RECTANGLE",
		Fills: []figmaPaint{
			{Type: "SOLID", Color: &figmaColor{R: 1, G: 0.33, B: 0.2, A: 1}},
		},
	}

	ns := SummarizeNode(node, 0)
	assert.Len(t, ns.Fills, 1)
	assert.Equal(t, "#FF5433", ns.Fills[0])
}

func TestSummarizeNode_withChildren(t *testing.T) {
	node := figmaNode{
		ID:   "0:1",
		Name: "Page",
		Type: "CANVAS",
		Children: []figmaNode{
			{ID: "1:1", Name: "Frame 1", Type: "FRAME"},
			{ID: "1:2", Name: "Frame 2", Type: "FRAME"},
		},
	}

	ns := SummarizeNode(node, 1)
	assert.Len(t, ns.Children, 2)
	assert.Equal(t, "Frame 1", ns.Children[0].Name)
	assert.Equal(t, "Frame 2", ns.Children[1].Name)
}

func TestSummarizeNode_depthLimiting(t *testing.T) {
	node := figmaNode{
		ID:   "0:1",
		Name: "Root",
		Type: "CANVAS",
		Children: []figmaNode{
			{
				ID:   "1:1",
				Name: "Child",
				Type: "FRAME",
				Children: []figmaNode{
					{ID: "2:1", Name: "Grandchild", Type: "TEXT"},
				},
			},
		},
	}

	// Depth 0 = no children
	ns := SummarizeNode(node, 0)
	assert.Nil(t, ns.Children)

	// Depth 1 = children but not grandchildren
	ns = SummarizeNode(node, 1)
	assert.Len(t, ns.Children, 1)
	assert.Nil(t, ns.Children[0].Children)

	// Depth 2 = children and grandchildren
	ns = SummarizeNode(node, 2)
	assert.Len(t, ns.Children, 1)
	assert.Len(t, ns.Children[0].Children, 1)
	assert.Equal(t, "Grandchild", ns.Children[0].Children[0].Name)
}

func TestSummarizeNode_noBoundingBox(t *testing.T) {
	node := figmaNode{ID: "0:1", Name: "Group", Type: "GROUP"}
	ns := SummarizeNode(node, 0)
	assert.Nil(t, ns.Size)
}

func TestSummarizeNode_noTypography(t *testing.T) {
	node := figmaNode{ID: "0:1", Name: "Box", Type: "RECTANGLE"}
	ns := SummarizeNode(node, 0)
	assert.Nil(t, ns.Typography)
}

func TestSummarizeNode_componentID(t *testing.T) {
	node := figmaNode{
		ID:          "3:1",
		Name:        "Button Instance",
		Type:        "INSTANCE",
		ComponentID: "comp:123",
	}

	ns := SummarizeNode(node, 0)
	assert.Equal(t, "comp:123", ns.ComponentID)
}

func TestColorToHex(t *testing.T) {
	tests := []struct {
		name  string
		color figmaColor
		want  string
	}{
		{
			name:  "red",
			color: figmaColor{R: 1, G: 0, B: 0, A: 1},
			want:  "#FF0000",
		},
		{
			name:  "green",
			color: figmaColor{R: 0, G: 1, B: 0, A: 1},
			want:  "#00FF00",
		},
		{
			name:  "blue",
			color: figmaColor{R: 0, G: 0, B: 1, A: 1},
			want:  "#0000FF",
		},
		{
			name:  "white",
			color: figmaColor{R: 1, G: 1, B: 1, A: 1},
			want:  "#FFFFFF",
		},
		{
			name:  "black",
			color: figmaColor{R: 0, G: 0, B: 0, A: 1},
			want:  "#000000",
		},
		{
			name:  "mid gray",
			color: figmaColor{R: 0.5, G: 0.5, B: 0.5, A: 1},
			want:  "#808080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ColorToHex(tt.color))
		})
	}
}

func TestFillsToStrings(t *testing.T) {
	visible := true
	invisible := false

	tests := []struct {
		name  string
		fills []figmaPaint
		want  []string
	}{
		{
			name:  "empty fills",
			fills: nil,
			want:  nil,
		},
		{
			name: "single solid fill",
			fills: []figmaPaint{
				{Type: "SOLID", Color: &figmaColor{R: 1, G: 0, B: 0, A: 1}},
			},
			want: []string{"#FF0000"},
		},
		{
			name: "invisible fill skipped",
			fills: []figmaPaint{
				{Type: "SOLID", Color: &figmaColor{R: 1, G: 0, B: 0, A: 1}, Visible: &invisible},
			},
			want: nil,
		},
		{
			name: "visible fill included",
			fills: []figmaPaint{
				{Type: "SOLID", Color: &figmaColor{R: 0, G: 1, B: 0, A: 1}, Visible: &visible},
			},
			want: []string{"#00FF00"},
		},
		{
			name: "gradient without color skipped",
			fills: []figmaPaint{
				{Type: "GRADIENT_LINEAR"},
			},
			want: nil,
		},
		{
			name: "multiple fills",
			fills: []figmaPaint{
				{Type: "SOLID", Color: &figmaColor{R: 1, G: 0, B: 0, A: 1}},
				{Type: "SOLID", Color: &figmaColor{R: 0, G: 0, B: 1, A: 1}},
			},
			want: []string{"#FF0000", "#0000FF"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FillsToStrings(tt.fills)
			assert.Equal(t, tt.want, got)
		})
	}
}
