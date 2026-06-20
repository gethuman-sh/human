package cmdclickup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/clickup"
)

// BuildClickUpCommands returns ClickUp-specific commands (hierarchy browsing,
// custom fields, members, linking) that are not part of the generic tracker interface.
func BuildClickUpCommands(deps cmdutil.Deps) []*cobra.Command {
	return []*cobra.Command{
		buildSpacesCmd(deps),
		buildFoldersCmd(deps),
		buildListsCmd(deps),
		buildMembersCmd(deps),
		buildFieldsCmd(deps),
		buildFieldSetCmd(deps),
	}
}

// resolveClickUpClient loads a ClickUp tracker instance and extracts the
// underlying *clickup.Client. It bypasses the safe/audit/destructive wrappers
// since these commands are read-only hierarchy browsing.
func resolveClickUpClient(cmd *cobra.Command, deps cmdutil.Deps) (*clickup.Client, error) {
	instances, err := deps.LoadInstances(config.DirProject)
	if err != nil {
		return nil, err
	}

	if inst := deps.InstanceFromFlags(cmd); inst != nil {
		instances = append(instances, *inst)
	}

	trackerName, _ := cmd.Root().PersistentFlags().GetString("tracker")
	instance, err := tracker.ResolveByKind("clickup", instances, trackerName)
	if err != nil {
		return nil, err
	}

	client, ok := instance.Provider.(*clickup.Client)
	if !ok {
		return nil, errors.WithDetails("resolved provider is not a ClickUp client")
	}
	return client, nil
}

func buildSpacesCmd(deps cmdutil.Deps) *cobra.Command {
	var table bool

	cmd := &cobra.Command{
		Use:     "spaces",
		Short:   "List spaces in the ClickUp workspace",
		Example: `  human clickup spaces`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			teamID := client.TeamID()
			if teamID == "" {
				return errors.WithDetails("team_id is required for listing spaces, set it in .humanconfig clickups[].team_id")
			}
			return runListSpaces(cmd.Context(), client, cmd.OutOrStdout(), teamID, table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildFoldersCmd(deps cmdutil.Deps) *cobra.Command {
	var spaceID string
	var table bool

	cmd := &cobra.Command{
		Use:     "folders",
		Short:   "List folders in a ClickUp space",
		Example: `  human clickup folders --space 12345`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			return runListFolders(cmd.Context(), client, cmd.OutOrStdout(), spaceID, table)
		},
	}
	cmd.Flags().StringVar(&spaceID, "space", "", "Space ID")
	_ = cmd.MarkFlagRequired("space")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildListsCmd(deps cmdutil.Deps) *cobra.Command {
	var folderID, spaceID string
	var table bool

	cmd := &cobra.Command{
		Use:   "lists",
		Short: "List lists in a ClickUp folder or space",
		Long: `List lists in a ClickUp folder (--folder) or folderless lists in a space (--space).
At least one of --folder or --space is required.`,
		Example: `  human clickup lists --folder 12345
  human clickup lists --space 67890`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if folderID == "" && spaceID == "" {
				return errors.WithDetails("at least one of --folder or --space is required")
			}
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			if folderID != "" {
				return runListLists(cmd.Context(), client, cmd.OutOrStdout(), folderID, table)
			}
			return runListFolderlessLists(cmd.Context(), client, cmd.OutOrStdout(), spaceID, table)
		},
	}
	cmd.Flags().StringVar(&folderID, "folder", "", "Folder ID")
	cmd.Flags().StringVar(&spaceID, "space", "", "Space ID (for folderless lists)")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildMembersCmd(deps cmdutil.Deps) *cobra.Command {
	var table bool

	cmd := &cobra.Command{
		Use:     "members",
		Short:   "List workspace members",
		Example: `  human clickup members`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			teamID := client.TeamID()
			if teamID == "" {
				return errors.WithDetails("team_id is required for listing members, set it in .humanconfig clickups[].team_id")
			}
			return runListMembers(cmd.Context(), client, cmd.OutOrStdout(), teamID, table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildFieldsCmd(deps cmdutil.Deps) *cobra.Command {
	var table bool

	cmd := &cobra.Command{
		Use:     "fields KEY",
		Short:   "List custom field values on a task",
		Example: `  human clickup fields abc123`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			return runGetFields(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildFieldSetCmd(deps cmdutil.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "field-set KEY FIELD_ID VALUE",
		Short:   "Set a custom field value on a task",
		Example: `  human clickup field-set abc123 field-uuid-here "new value"`,
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveClickUpClient(cmd, deps)
			if err != nil {
				return err
			}
			return runSetField(cmd.Context(), client, cmd.OutOrStdout(), args[0], args[1], args[2])
		},
	}
	return cmd
}

// --- Business logic ---

func runListSpaces(ctx context.Context, client *clickup.Client, out io.Writer, teamID string, table bool) error {
	spaces, err := client.ListSpaces(ctx, teamID)
	if err != nil {
		return err
	}
	if table {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME")
		for _, s := range spaces {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", s.ID, s.Name)
		}
		return w.Flush()
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(spaces)
}

func runListFolders(ctx context.Context, client *clickup.Client, out io.Writer, spaceID string, table bool) error {
	folders, err := client.ListFolders(ctx, spaceID)
	if err != nil {
		return err
	}
	if table {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME")
		for _, f := range folders {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", f.ID, f.Name)
		}
		return w.Flush()
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(folders)
}

func runListLists(ctx context.Context, client *clickup.Client, out io.Writer, folderID string, table bool) error {
	lists, err := client.ListLists(ctx, folderID)
	if err != nil {
		return err
	}
	return printLists(out, lists, table)
}

func runListFolderlessLists(ctx context.Context, client *clickup.Client, out io.Writer, spaceID string, table bool) error {
	lists, err := client.ListFolderlessLists(ctx, spaceID)
	if err != nil {
		return err
	}
	return printLists(out, lists, table)
}

func printLists(out io.Writer, lists []clickup.List, table bool) error {
	if table {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME")
		for _, l := range lists {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", l.ID, l.Name)
		}
		return w.Flush()
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(lists)
}

func runListMembers(ctx context.Context, client *clickup.Client, out io.Writer, teamID string, table bool) error {
	members, err := client.ListWorkspaceMembers(ctx, teamID)
	if err != nil {
		return err
	}
	if table {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tUSERNAME\tEMAIL")
		for _, m := range members {
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\n", m.ID, m.Username, m.Email)
		}
		return w.Flush()
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(members)
}

func runGetFields(ctx context.Context, client *clickup.Client, out io.Writer, key string, table bool) error {
	fields, err := client.GetCustomFields(ctx, key)
	if err != nil {
		return err
	}
	if table {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tTYPE\tVALUE")
		for _, f := range fields {
			val := fmt.Sprintf("%v", f.Value)
			if f.Value == nil {
				val = ""
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.ID, f.Name, f.Type, val)
		}
		return w.Flush()
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(fields)
}

func runSetField(ctx context.Context, client *clickup.Client, out io.Writer, taskID, fieldID, value string) error {
	if err := client.SetCustomField(ctx, taskID, fieldID, value); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Set field %s on %s\n", fieldID, taskID)
	return nil
}
