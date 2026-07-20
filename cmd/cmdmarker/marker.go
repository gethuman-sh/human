// Package cmdmarker surfaces the [human:*] marker protocol as CLI commands so
// pipeline agents post and read structured handoff comments through one
// validated grammar instead of reproducing prose templates.
package cmdmarker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BuildMarkerCmd creates the top-level "marker" command.
func BuildMarkerCmd(deps cmdutil.Deps) *cobra.Command {
	markerCmd := &cobra.Command{
		Use:   "marker",
		Short: "Post and read [human:*] pipeline marker comments on a ticket",
		Long: `Post and read the structured [human:*] marker comments through which
pipeline stages hand work to each other on a ticket.

Known types (validated): ` + strings.Join(marker.KnownTypes(), ", ") + `
Unknown types are allowed so new pipeline stages need no CLI release.`,
	}
	markerCmd.AddCommand(buildPostCmd(deps), buildShowCmd(deps), buildListCmd(deps))
	return markerCmd
}

func buildPostCmd(deps cmdutil.Deps) *cobra.Command {
	var fields []string
	var head, body, bodyFile string
	cmd := &cobra.Command{
		Use:   "post KEY TYPE",
		Short: "Post a [human:TYPE] marker comment (validated for known types)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			bodyText, err := resolveBody(body, bodyFile, cmd.InOrStdin())
			if err != nil {
				return err
			}
			return RunMarkerPost(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key, args[1], head, fields, bodyText)
		},
	}
	cmd.Flags().StringArrayVar(&fields, "field", nil, "Field line as key=value (repeatable; posting order preserved)")
	cmd.Flags().StringVar(&head, "head", "", "Token on the header line ([human:TYPE] TOKEN)")
	cmd.Flags().StringVar(&body, "body", "", "Free-form body after the field block")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the body from a file, or - for stdin")
	return cmd
}

func buildShowCmd(deps cmdutil.Deps) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "show KEY TYPE",
		Short: "Print the newest [human:TYPE] marker on a ticket (latest wins)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunMarkerShow(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key, args[1], raw)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "Print the marker comment body verbatim instead of parsed JSON")
	return cmd
}

func buildListCmd(deps cmdutil.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list KEY",
		Short: "List all [human:*] markers on a ticket, newest first (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunMarkerList(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key)
		},
	}
	return cmd
}

// RunMarkerPost validates, renders, and posts a marker comment, echoing the
// rendered body so the caller sees exactly what landed on the ticket.
func RunMarkerPost(ctx context.Context, p tracker.Provider, out io.Writer, key, markerType, head string, fieldArgs []string, body string) error {
	fields, order, err := parseFieldArgs(fieldArgs)
	if err != nil {
		return err
	}
	m := marker.Marker{Type: markerType, Head: head, Fields: fields, Body: body}
	if err := marker.Validate(m); err != nil {
		return err
	}
	rendered := marker.Render(m, order)
	if _, err := p.AddComment(ctx, key, rendered); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, rendered)
	return err
}

// RunMarkerShow prints the newest marker of markerType on the ticket.
func RunMarkerShow(ctx context.Context, p tracker.Provider, out io.Writer, key, markerType string, raw bool) error {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	m, ok := marker.Latest(comments, markerType)
	if !ok {
		return errors.WithDetails("no such marker on ticket", "key", key, "type", markerType)
	}
	if raw {
		_, err = fmt.Fprintln(out, marker.Render(m, nil))
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// RunMarkerList prints every marker on the ticket, newest first.
func RunMarkerList(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	markers := marker.All(comments)
	if markers == nil {
		markers = []marker.Marker{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(markers)
}

// parseFieldArgs turns repeated key=value flags into a field map plus the
// caller's ordering, which Render preserves.
func parseFieldArgs(args []string) (map[string]string, []string, error) {
	if len(args) == 0 {
		return nil, nil, nil
	}
	fields := make(map[string]string, len(args))
	order := make([]string, 0, len(args))
	for _, arg := range args {
		key, value, found := strings.Cut(arg, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, nil, errors.WithDetails("field must be key=value", "got", arg)
		}
		key = strings.TrimSpace(key)
		if _, dup := fields[key]; !dup {
			order = append(order, key)
		}
		fields[key] = value
	}
	return fields, order, nil
}

// resolveBody picks the marker body from --body, --body-file, or stdin ("-").
func resolveBody(body, bodyFile string, stdin io.Reader) (string, error) {
	if body != "" && bodyFile != "" {
		return "", errors.WithDetails("use either --body or --body-file, not both")
	}
	if bodyFile == "" {
		return body, nil
	}
	if bodyFile == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", errors.WrapWithDetails(err, "reading body from stdin")
		}
		return string(data), nil
	}
	data, err := os.ReadFile(bodyFile) // #nosec G304 -- path is an explicit user-supplied CLI argument
	if err != nil {
		return "", errors.WrapWithDetails(err, "reading body file", "path", bodyFile)
	}
	return string(data), nil
}
