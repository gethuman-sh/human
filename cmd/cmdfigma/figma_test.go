package cmdfigma

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/knowledge/figma"
)

// --- mock figma client ---

type mockFigmaClient struct {
	getFileFn           func(ctx context.Context, fileKey string) (*figma.FileSummary, error)
	getNodesFn          func(ctx context.Context, fileKey string, nodeIDs []string) ([]figma.NodeSummary, error)
	getFileComponentsFn func(ctx context.Context, fileKey string) ([]figma.Component, error)
	getFileCommentsFn   func(ctx context.Context, fileKey string) ([]figma.FileComment, error)
	exportImagesFn      func(ctx context.Context, fileKey string, nodeIDs []string, format string) ([]figma.ImageExport, error)
	listProjectsFn      func(ctx context.Context, teamID string) ([]figma.Project, error)
	listProjectFilesFn  func(ctx context.Context, projectID string) ([]figma.ProjectFile, error)
}

func (m *mockFigmaClient) GetFile(ctx context.Context, fileKey string) (*figma.FileSummary, error) {
	return m.getFileFn(ctx, fileKey)
}

func (m *mockFigmaClient) GetNodes(ctx context.Context, fileKey string, nodeIDs []string) ([]figma.NodeSummary, error) {
	return m.getNodesFn(ctx, fileKey, nodeIDs)
}

func (m *mockFigmaClient) GetFileComponents(ctx context.Context, fileKey string) ([]figma.Component, error) {
	return m.getFileComponentsFn(ctx, fileKey)
}

func (m *mockFigmaClient) GetFileComments(ctx context.Context, fileKey string) ([]figma.FileComment, error) {
	return m.getFileCommentsFn(ctx, fileKey)
}

func (m *mockFigmaClient) ExportImages(ctx context.Context, fileKey string, nodeIDs []string, format string) ([]figma.ImageExport, error) {
	return m.exportImagesFn(ctx, fileKey, nodeIDs, format)
}

func (m *mockFigmaClient) ListProjects(ctx context.Context, teamID string) ([]figma.Project, error) {
	return m.listProjectsFn(ctx, teamID)
}

func (m *mockFigmaClient) ListProjectFiles(ctx context.Context, projectID string) ([]figma.ProjectFile, error) {
	return m.listProjectFilesFn(ctx, projectID)
}

// --- file get tests ---

func TestRunFigmaFileGet_JSON(t *testing.T) {
	summary := &figma.FileSummary{
		Name:           "My Design",
		LastModified:   "2025-01-15T10:30:00Z",
		Version:        "123",
		ComponentCount: 5,
		Pages: []figma.PageSummary{
			{ID: "0:1", Name: "Page 1", ChildCount: 3},
		},
	}
	client := &mockFigmaClient{
		getFileFn: func(_ context.Context, fileKey string) (*figma.FileSummary, error) {
			assert.Equal(t, "abc123", fileKey)
			return summary, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileGet(context.Background(), client, &buf, "abc123", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "My Design"`)
	assert.Contains(t, buf.String(), `"component_count": 5`)
}

func TestRunFigmaFileGet_Table(t *testing.T) {
	summary := &figma.FileSummary{
		Name:           "My Design",
		LastModified:   "2025-01-15T10:30:00Z",
		Version:        "v1",
		ComponentCount: 2,
		Pages: []figma.PageSummary{
			{ID: "0:1", Name: "Page 1", ChildCount: 3},
		},
	}
	client := &mockFigmaClient{
		getFileFn: func(_ context.Context, _ string) (*figma.FileSummary, error) {
			return summary, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileGet(context.Background(), client, &buf, "abc", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Name:")
	assert.Contains(t, buf.String(), "My Design")
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Page 1")
}

func TestRunFigmaFileGet_Error(t *testing.T) {
	client := &mockFigmaClient{
		getFileFn: func(_ context.Context, _ string) (*figma.FileSummary, error) {
			return nil, fmt.Errorf("file not found")
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileGet(context.Background(), client, &buf, "bad", false)
	assert.EqualError(t, err, "file not found")
}

// --- file nodes tests ---

func TestRunFigmaFileNodes_JSON(t *testing.T) {
	nodes := []figma.NodeSummary{
		{ID: "0:1", Name: "Header", Type: "FRAME", Size: &figma.Size{Width: 1440, Height: 80}},
	}
	client := &mockFigmaClient{
		getNodesFn: func(_ context.Context, fileKey string, nodeIDs []string) ([]figma.NodeSummary, error) {
			assert.Equal(t, "abc", fileKey)
			assert.Equal(t, []string{"0:1"}, nodeIDs)
			return nodes, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileNodes(context.Background(), client, &buf, "abc", []string{"0:1"}, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "Header"`)
}

func TestRunFigmaFileNodes_Table(t *testing.T) {
	nodes := []figma.NodeSummary{
		{ID: "0:1", Name: "Header", Type: "FRAME", Size: &figma.Size{Width: 1440, Height: 80}},
	}
	client := &mockFigmaClient{
		getNodesFn: func(_ context.Context, _ string, _ []string) ([]figma.NodeSummary, error) {
			return nodes, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileNodes(context.Background(), client, &buf, "abc", []string{"0:1"}, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Header")
	assert.Contains(t, buf.String(), "1440x80")
}

func TestRunFigmaFileNodes_Error(t *testing.T) {
	client := &mockFigmaClient{
		getNodesFn: func(_ context.Context, _ string, _ []string) ([]figma.NodeSummary, error) {
			return nil, fmt.Errorf("nodes failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileNodes(context.Background(), client, &buf, "abc", []string{"0:1"}, false)
	assert.EqualError(t, err, "nodes failed")
}

func TestRunFigmaFileNodes_Empty(t *testing.T) {
	client := &mockFigmaClient{
		getNodesFn: func(_ context.Context, _ string, _ []string) ([]figma.NodeSummary, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileNodes(context.Background(), client, &buf, "abc", []string{"0:1"}, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No nodes found")
}

// --- file components tests ---

func TestRunFigmaFileComponents_JSON(t *testing.T) {
	components := []figma.Component{
		{Key: "k1", NodeID: "1:1", Name: "Button", Description: "Primary", Page: "Design System", Frame: "Components"},
	}
	client := &mockFigmaClient{
		getFileComponentsFn: func(_ context.Context, fileKey string) ([]figma.Component, error) {
			assert.Equal(t, "abc", fileKey)
			return components, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComponents(context.Background(), client, &buf, "abc", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "Button"`)
	assert.Contains(t, buf.String(), `"description": "Primary"`)
}

func TestRunFigmaFileComponents_Table(t *testing.T) {
	components := []figma.Component{
		{Key: "k1", Name: "Button", Page: "DS", Frame: "Components", Description: "Primary"},
	}
	client := &mockFigmaClient{
		getFileComponentsFn: func(_ context.Context, _ string) ([]figma.Component, error) {
			return components, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComponents(context.Background(), client, &buf, "abc", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KEY")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Button")
}

func TestRunFigmaFileComponents_Error(t *testing.T) {
	client := &mockFigmaClient{
		getFileComponentsFn: func(_ context.Context, _ string) ([]figma.Component, error) {
			return nil, fmt.Errorf("components failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComponents(context.Background(), client, &buf, "abc", false)
	assert.EqualError(t, err, "components failed")
}

func TestRunFigmaFileComponents_Empty(t *testing.T) {
	client := &mockFigmaClient{
		getFileComponentsFn: func(_ context.Context, _ string) ([]figma.Component, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComponents(context.Background(), client, &buf, "abc", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No components found")
}

// --- file comments tests ---

func TestRunFigmaFileComments_JSON(t *testing.T) {
	comments := []figma.FileComment{
		{ID: "c1", Author: "Alice", Message: "Looks good!", CreatedAt: "2025-01-15T10:30:00Z", Resolved: false, NodeID: "1:1"},
	}
	client := &mockFigmaClient{
		getFileCommentsFn: func(_ context.Context, fileKey string) ([]figma.FileComment, error) {
			assert.Equal(t, "abc", fileKey)
			return comments, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComments(context.Background(), client, &buf, "abc", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"author": "Alice"`)
	assert.Contains(t, buf.String(), `"message": "Looks good!"`)
}

func TestRunFigmaFileComments_Table(t *testing.T) {
	comments := []figma.FileComment{
		{ID: "c1", Author: "Alice", Message: "Looks good!", Resolved: false},
	}
	client := &mockFigmaClient{
		getFileCommentsFn: func(_ context.Context, _ string) ([]figma.FileComment, error) {
			return comments, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComments(context.Background(), client, &buf, "abc", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "AUTHOR")
	assert.Contains(t, buf.String(), "Alice")
	assert.Contains(t, buf.String(), "Looks good!")
}

func TestRunFigmaFileComments_Error(t *testing.T) {
	client := &mockFigmaClient{
		getFileCommentsFn: func(_ context.Context, _ string) ([]figma.FileComment, error) {
			return nil, fmt.Errorf("comments failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComments(context.Background(), client, &buf, "abc", false)
	assert.EqualError(t, err, "comments failed")
}

func TestRunFigmaFileComments_Empty(t *testing.T) {
	client := &mockFigmaClient{
		getFileCommentsFn: func(_ context.Context, _ string) ([]figma.FileComment, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileComments(context.Background(), client, &buf, "abc", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No comments found")
}

// --- file image tests ---

func TestRunFigmaFileImage_JSON(t *testing.T) {
	exports := []figma.ImageExport{
		{NodeID: "0:1", URL: "https://figma.com/images/abc"},
	}
	client := &mockFigmaClient{
		exportImagesFn: func(_ context.Context, fileKey string, nodeIDs []string, format string) ([]figma.ImageExport, error) {
			assert.Equal(t, "abc", fileKey)
			assert.Equal(t, []string{"0:1"}, nodeIDs)
			assert.Equal(t, "png", format)
			return exports, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileImage(context.Background(), client, &buf, "abc", []string{"0:1"}, "png")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"node_id": "0:1"`)
	assert.Contains(t, buf.String(), `"url": "https://figma.com/images/abc"`)
}

func TestRunFigmaFileImage_Error(t *testing.T) {
	client := &mockFigmaClient{
		exportImagesFn: func(_ context.Context, _ string, _ []string, _ string) ([]figma.ImageExport, error) {
			return nil, fmt.Errorf("export failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaFileImage(context.Background(), client, &buf, "abc", []string{"0:1"}, "png")
	assert.EqualError(t, err, "export failed")
}

// --- projects list tests ---

func TestRunFigmaProjectsList_JSON(t *testing.T) {
	projects := []figma.Project{
		{ID: 1, Name: "Mobile App"},
		{ID: 2, Name: "Web App"},
	}
	client := &mockFigmaClient{
		listProjectsFn: func(_ context.Context, teamID string) ([]figma.Project, error) {
			assert.Equal(t, "team123", teamID)
			return projects, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectsList(context.Background(), client, &buf, "team123", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "Mobile App"`)
	assert.Contains(t, buf.String(), `"id": 2`)
}

func TestRunFigmaProjectsList_Table(t *testing.T) {
	projects := []figma.Project{
		{ID: 1, Name: "Mobile App"},
	}
	client := &mockFigmaClient{
		listProjectsFn: func(_ context.Context, _ string) ([]figma.Project, error) {
			return projects, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectsList(context.Background(), client, &buf, "team123", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ID")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Mobile App")
}

func TestRunFigmaProjectsList_Error(t *testing.T) {
	client := &mockFigmaClient{
		listProjectsFn: func(_ context.Context, _ string) ([]figma.Project, error) {
			return nil, fmt.Errorf("projects failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectsList(context.Background(), client, &buf, "team123", false)
	assert.EqualError(t, err, "projects failed")
}

func TestRunFigmaProjectsList_Empty(t *testing.T) {
	client := &mockFigmaClient{
		listProjectsFn: func(_ context.Context, _ string) ([]figma.Project, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectsList(context.Background(), client, &buf, "team123", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No projects found")
}

// --- project files tests ---

func TestRunFigmaProjectFiles_JSON(t *testing.T) {
	files := []figma.ProjectFile{
		{Key: "file1", Name: "Homepage", ThumbnailURL: "https://thumb/1", LastModified: "2025-01-15T10:30:00Z"},
	}
	client := &mockFigmaClient{
		listProjectFilesFn: func(_ context.Context, projectID string) ([]figma.ProjectFile, error) {
			assert.Equal(t, "42", projectID)
			return files, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectFiles(context.Background(), client, &buf, "42", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"name": "Homepage"`)
	assert.Contains(t, buf.String(), `"key": "file1"`)
}

func TestRunFigmaProjectFiles_Table(t *testing.T) {
	files := []figma.ProjectFile{
		{Key: "file1", Name: "Homepage", LastModified: "2025-01-15T10:30:00Z"},
	}
	client := &mockFigmaClient{
		listProjectFilesFn: func(_ context.Context, _ string) ([]figma.ProjectFile, error) {
			return files, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectFiles(context.Background(), client, &buf, "42", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "KEY")
	assert.Contains(t, buf.String(), "NAME")
	assert.Contains(t, buf.String(), "Homepage")
}

func TestRunFigmaProjectFiles_Error(t *testing.T) {
	client := &mockFigmaClient{
		listProjectFilesFn: func(_ context.Context, _ string) ([]figma.ProjectFile, error) {
			return nil, fmt.Errorf("files failed")
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectFiles(context.Background(), client, &buf, "42", false)
	assert.EqualError(t, err, "files failed")
}

func TestRunFigmaProjectFiles_Empty(t *testing.T) {
	client := &mockFigmaClient{
		listProjectFilesFn: func(_ context.Context, _ string) ([]figma.ProjectFile, error) {
			return nil, nil
		},
	}

	var buf bytes.Buffer
	err := runFigmaProjectFiles(context.Background(), client, &buf, "42", true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No files found")
}

// --- print function tests ---

func TestPrintFigmaNodesTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printFigmaNodesTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No nodes found")
}

func TestPrintFigmaComponentsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printFigmaComponentsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No components found")
}

func TestPrintFigmaCommentsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printFigmaCommentsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No comments found")
}

func TestPrintFigmaProjectsTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printFigmaProjectsTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No projects found")
}

func TestPrintFigmaProjectFilesTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printFigmaProjectFilesTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No files found")
}

// --- splitIDs tests ---

func TestSplitIDs(t *testing.T) {
	assert.Nil(t, cmdutil.SplitIDs(""))
	assert.Equal(t, []string{"0:1"}, cmdutil.SplitIDs("0:1"))
	assert.Equal(t, []string{"0:1", "1:2"}, cmdutil.SplitIDs("0:1,1:2"))
	assert.Equal(t, []string{"0:1", "1:2"}, cmdutil.SplitIDs("0:1, 1:2"))
	assert.Equal(t, []string{"0:1"}, cmdutil.SplitIDs("0:1,"))
}

// --- command tree tests ---

func TestFigmaCmd_hasSubcommands(t *testing.T) {
	cmd := BuildFigmaCommands()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["file"], "expected 'file' subcommand")
	assert.True(t, subNames["projects"], "expected 'projects' subcommand")
	assert.True(t, subNames["project"], "expected 'project' subcommand")
}

func TestFigmaFileCmd_hasSubcommands(t *testing.T) {
	cmd := BuildFigmaCommands()
	var fileCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "file" {
			fileCmd = sub
			break
		}
	}
	require.NotNil(t, fileCmd)

	subNames := make(map[string]bool)
	for _, sub := range fileCmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["get FILE_KEY"], "expected 'get' subcommand")
	assert.True(t, subNames["nodes FILE_KEY"], "expected 'nodes' subcommand")
	assert.True(t, subNames["components FILE_KEY"], "expected 'components' subcommand")
	assert.True(t, subNames["comments FILE_KEY"], "expected 'comments' subcommand")
	assert.True(t, subNames["image FILE_KEY"], "expected 'image' subcommand")
}
