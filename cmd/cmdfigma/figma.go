package cmdfigma

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/knowledge/figma"
)

// --- Interfaces ---

// figmaFileGetter retrieves file metadata.
type figmaFileGetter interface {
	GetFile(ctx context.Context, fileKey string) (*figma.FileSummary, error)
}

// figmaNodeGetter retrieves specific nodes.
type figmaNodeGetter interface {
	GetNodes(ctx context.Context, fileKey string, nodeIDs []string) ([]figma.NodeSummary, error)
}

// figmaComponentLister lists published components.
type figmaComponentLister interface {
	GetFileComponents(ctx context.Context, fileKey string) ([]figma.Component, error)
}

// figmaCommentLister lists file comments.
type figmaCommentLister interface {
	GetFileComments(ctx context.Context, fileKey string) ([]figma.FileComment, error)
}

// figmaImageExporter exports nodes as images.
type figmaImageExporter interface {
	ExportImages(ctx context.Context, fileKey string, nodeIDs []string, format string) ([]figma.ImageExport, error)
}

// figmaProjectLister lists team projects.
type figmaProjectLister interface {
	ListProjects(ctx context.Context, teamID string) ([]figma.Project, error)
}

// figmaProjectFileLister lists files in a project.
type figmaProjectFileLister interface {
	ListProjectFiles(ctx context.Context, projectID string) ([]figma.ProjectFile, error)
}

// --- Command builders ---

// BuildFigmaCommands returns the top-level "figma" command tree.
func BuildFigmaCommands() *cobra.Command {
	figmaCmd := &cobra.Command{
		Use:   "figma",
		Short: "Figma design tools",
	}

	figmaCmd.PersistentFlags().String("figma", "", "Named Figma instance from .humanconfig")

	figmaCmd.AddCommand(BuildFigmaFileCommands())
	figmaCmd.AddCommand(BuildFigmaProjectsCmd())
	figmaCmd.AddCommand(BuildFigmaProjectCmd())

	return figmaCmd
}

// BuildFigmaFileCommands returns the "file" subcommand group.
func BuildFigmaFileCommands() *cobra.Command {
	fileCmd := &cobra.Command{
		Use:   "file",
		Short: "File operations",
	}

	fileCmd.AddCommand(BuildFigmaFileGetCmd())
	fileCmd.AddCommand(BuildFigmaFileNodesCmd())
	fileCmd.AddCommand(BuildFigmaFileComponentsCmd())
	fileCmd.AddCommand(BuildFigmaFileCommentsCmd())
	fileCmd.AddCommand(BuildFigmaFileImageCmd())

	return fileCmd
}

// BuildFigmaFileGetCmd returns the "file get" command.
func BuildFigmaFileGetCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "get FILE_KEY",
		Short: "Get file metadata and page listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			return runFigmaFileGet(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildFigmaFileNodesCmd returns the "file nodes" command.
func BuildFigmaFileNodesCmd() *cobra.Command {
	var table bool
	var ids string
	cmd := &cobra.Command{
		Use:   "nodes FILE_KEY",
		Short: "Inspect specific nodes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			nodeIDs := cmdutil.SplitIDs(ids)
			return runFigmaFileNodes(cmd.Context(), client, cmd.OutOrStdout(), args[0], nodeIDs, table)
		},
	}
	cmd.Flags().StringVar(&ids, "ids", "", "Comma-separated node IDs (e.g. 0:1,1:234)")
	_ = cmd.MarkFlagRequired("ids")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildFigmaFileComponentsCmd returns the "file components" command.
func BuildFigmaFileComponentsCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "components FILE_KEY",
		Short: "List published components in a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			return runFigmaFileComponents(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildFigmaFileCommentsCmd returns the "file comments" command.
func BuildFigmaFileCommentsCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "comments FILE_KEY",
		Short: "List comments on a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			return runFigmaFileComments(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildFigmaFileImageCmd returns the "file image" command.
func BuildFigmaFileImageCmd() *cobra.Command {
	var ids string
	var format string
	cmd := &cobra.Command{
		Use:   "image FILE_KEY",
		Short: "Export node as image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			nodeIDs := cmdutil.SplitIDs(ids)
			return runFigmaFileImage(cmd.Context(), client, cmd.OutOrStdout(), args[0], nodeIDs, format)
		},
	}
	cmd.Flags().StringVar(&ids, "ids", "", "Comma-separated node IDs to export")
	_ = cmd.MarkFlagRequired("ids")
	cmd.Flags().StringVar(&format, "format", "png", "Image format (png, jpg, svg, pdf)")
	return cmd
}

// BuildFigmaProjectsCmd returns the "projects" subcommand group.
func BuildFigmaProjectsCmd() *cobra.Command {
	projectsCmd := &cobra.Command{
		Use:   "projects",
		Short: "Team project operations",
	}

	var table bool
	var teamID string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List team projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			return runFigmaProjectsList(cmd.Context(), client, cmd.OutOrStdout(), teamID, table)
		},
	}
	listCmd.Flags().StringVar(&teamID, "team", "", "Team ID")
	_ = listCmd.MarkFlagRequired("team")
	listCmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")

	projectsCmd.AddCommand(listCmd)
	return projectsCmd
}

// BuildFigmaProjectCmd returns the "project" subcommand group.
func BuildFigmaProjectCmd() *cobra.Command {
	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Project operations",
	}

	var table bool
	filesCmd := &cobra.Command{
		Use:   "files PROJECT_ID",
		Short: "List files in a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveFigmaClient(cmd)
			if err != nil {
				return err
			}
			return runFigmaProjectFiles(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	filesCmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")

	projectCmd.AddCommand(filesCmd)
	return projectCmd
}

// --- Client resolution ---

func resolveFigmaClient(cmd *cobra.Command) (*figma.Client, error) {
	name, _ := cmd.Flags().GetString("figma")

	instances, err := figma.LoadInstances(".")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.WithDetails("no Figma instances configured, add figmas: to .humanconfig.yaml")
	}

	if name != "" {
		for _, inst := range instances {
			if inst.Name == name {
				return inst.Client, nil
			}
		}
		return nil, errors.WithDetails("Figma instance not found", "name", name)
	}

	return instances[0].Client, nil
}

// --- Business logic functions ---

func runFigmaFileGet(ctx context.Context, client figmaFileGetter, out io.Writer, fileKey string, table bool) error {
	summary, err := client.GetFile(ctx, fileKey)
	if err != nil {
		return err
	}
	if table {
		return printFigmaFileSummaryTable(out, summary)
	}
	return cmdutil.PrintJSON(out, summary)
}

func runFigmaFileNodes(ctx context.Context, client figmaNodeGetter, out io.Writer, fileKey string, nodeIDs []string, table bool) error {
	nodes, err := client.GetNodes(ctx, fileKey, nodeIDs)
	if err != nil {
		return err
	}
	if table {
		return printFigmaNodesTable(out, nodes)
	}
	return cmdutil.PrintJSON(out, nodes)
}

func runFigmaFileComponents(ctx context.Context, client figmaComponentLister, out io.Writer, fileKey string, table bool) error {
	components, err := client.GetFileComponents(ctx, fileKey)
	if err != nil {
		return err
	}
	if table {
		return printFigmaComponentsTable(out, components)
	}
	return cmdutil.PrintJSON(out, components)
}

func runFigmaFileComments(ctx context.Context, client figmaCommentLister, out io.Writer, fileKey string, table bool) error {
	comments, err := client.GetFileComments(ctx, fileKey)
	if err != nil {
		return err
	}
	if table {
		return printFigmaCommentsTable(out, comments)
	}
	return cmdutil.PrintJSON(out, comments)
}

func runFigmaFileImage(ctx context.Context, client figmaImageExporter, out io.Writer, fileKey string, nodeIDs []string, format string) error {
	exports, err := client.ExportImages(ctx, fileKey, nodeIDs, format)
	if err != nil {
		return err
	}
	return cmdutil.PrintJSON(out, exports)
}

func runFigmaProjectsList(ctx context.Context, client figmaProjectLister, out io.Writer, teamID string, table bool) error {
	projects, err := client.ListProjects(ctx, teamID)
	if err != nil {
		return err
	}
	if table {
		return printFigmaProjectsTable(out, projects)
	}
	return cmdutil.PrintJSON(out, projects)
}

func runFigmaProjectFiles(ctx context.Context, client figmaProjectFileLister, out io.Writer, projectID string, table bool) error {
	files, err := client.ListProjectFiles(ctx, projectID)
	if err != nil {
		return err
	}
	if table {
		return printFigmaProjectFilesTable(out, files)
	}
	return cmdutil.PrintJSON(out, files)
}

// --- Output formatters ---

func printFigmaFileSummaryTable(out io.Writer, s *figma.FileSummary) error {
	_, _ = fmt.Fprintf(out, "Name:        %s\n", s.Name)
	_, _ = fmt.Fprintf(out, "Version:     %s\n", s.Version)
	_, _ = fmt.Fprintf(out, "Modified:    %s\n", s.LastModified)
	_, _ = fmt.Fprintf(out, "Components:  %d\n", s.ComponentCount)
	_, _ = fmt.Fprintln(out)

	if len(s.Pages) == 0 {
		_, _ = fmt.Fprintln(out, "No pages found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tCHILDREN")
	for _, p := range s.Pages {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\n", p.ID, p.Name, p.ChildCount)
	}
	return w.Flush()
}

func printFigmaNodesTable(out io.Writer, nodes []figma.NodeSummary) error {
	if len(nodes) == 0 {
		_, _ = fmt.Fprintln(out, "No nodes found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tTYPE\tSIZE\tTEXT")
	for _, n := range nodes {
		size := ""
		if n.Size != nil {
			size = fmt.Sprintf("%.0fx%.0f", n.Size.Width, n.Size.Height)
		}
		text := cmdutil.TruncateRunes(n.Text, 50)
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n.ID, n.Name, n.Type, size, text)
	}
	return w.Flush()
}

func printFigmaComponentsTable(out io.Writer, components []figma.Component) error {
	if len(components) == 0 {
		_, _ = fmt.Fprintln(out, "No components found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tNAME\tPAGE\tFRAME\tDESCRIPTION")
	for _, c := range components {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Key, c.Name, c.Page, c.Frame, c.Description)
	}
	return w.Flush()
}

func printFigmaCommentsTable(out io.Writer, comments []figma.FileComment) error {
	if len(comments) == 0 {
		_, _ = fmt.Fprintln(out, "No comments found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tAUTHOR\tRESOLVED\tMESSAGE")
	for _, c := range comments {
		resolved := "no"
		if c.Resolved {
			resolved = "yes"
		}
		msg := cmdutil.TruncateRunes(c.Message, 60)
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.ID, c.Author, resolved, msg)
	}
	return w.Flush()
}

func printFigmaProjectsTable(out io.Writer, projects []figma.Project) error {
	if len(projects) == 0 {
		_, _ = fmt.Fprintln(out, "No projects found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME")
	for _, p := range projects {
		_, _ = fmt.Fprintf(w, "%d\t%s\n", p.ID, p.Name)
	}
	return w.Flush()
}

func printFigmaProjectFilesTable(out io.Writer, files []figma.ProjectFile) error {
	if len(files) == 0 {
		_, _ = fmt.Fprintln(out, "No files found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tNAME\tMODIFIED")
	for _, f := range files {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", f.Key, f.Name, f.LastModified)
	}
	return w.Flush()
}
