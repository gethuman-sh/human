// Package cmdpipeline surfaces the scan-pipeline runtime (internal/pipeline)
// as local CLI commands, so parallel finder agents share one safe
// implementation of ID allocation, dedup, state, and cleanup.
package cmdpipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/pipeline"
)

// BuildPipelineCmd creates the top-level "pipeline" command.
func BuildPipelineCmd() *cobra.Command {
	pipelineCmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Shared runtime for multi-agent scan pipelines (findings, state, reports)",
	}
	pipelineCmd.AddCommand(buildInitCmd(), buildAppendCmd(), buildCountCmd(), buildStateCmd(), buildReportCmd(), buildCleanupCmd())
	return pipelineCmd
}

func buildInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init NAME",
		Short: "Create the pipeline workspace under .human/NAME and print its paths",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), map[string]string{
				"root":       w.Root(),
				"candidates": w.CandidatesPath(),
				"state":      w.StatePath(),
			})
		},
	}
}

func buildAppendCmd() *cobra.Command {
	var file, category, title, bodyFile string
	var line int
	cmd := &cobra.Command{
		Use:   "append NAME",
		Short: "Append a finding with a race-free ID; exact duplicates (file+line+category) are dropped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			body, err := resolveBody(bodyFile, cmd.InOrStdin())
			if err != nil {
				return err
			}
			id, duplicate, err := w.Append(pipeline.Finding{File: file, Line: line, Category: category, Title: title, Body: body})
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), map[string]any{"id": id, "duplicate": duplicate})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "File the finding is in")
	cmd.Flags().IntVar(&line, "line", 0, "Line the finding anchors to")
	cmd.Flags().StringVar(&category, "category", "", "Finding category (dedup key together with file and line)")
	cmd.Flags().StringVar(&title, "title", "", "One-line finding title")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Finding body from a file, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("category")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func buildCountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "count NAME",
		Short: "Print the number of candidate findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			count, err := w.Count()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), count)
			return err
		},
	}
}

func buildStateCmd() *cobra.Command {
	stateCmd := &cobra.Command{
		Use:   "state",
		Short: "Read or write the pipeline's shared key-value state",
	}
	getCmd := &cobra.Command{
		Use:   "get NAME KEY",
		Short: "Print the value stored for KEY (empty when unset)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			value, err := w.StateGet(args[1])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), value)
			return err
		},
	}
	setCmd := &cobra.Command{
		Use:   "set NAME KEY VALUE",
		Short: "Store KEY: VALUE, replacing any existing entry",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			return w.StateSet(args[1], args[2])
		},
	}
	stateCmd.AddCommand(getCmd, setCmd)
	return stateCmd
}

func buildReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report NAME",
		Short: "Print the timestamped final-report path for the triage agent to write",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), w.ReportPath(time.Now()))
			return err
		},
	}
}

func buildCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup NAME",
		Short: "Remove the pipeline's intermediate dot-files, keeping final reports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pipeline.Open(".", args[0])
			if err != nil {
				return err
			}
			return w.Cleanup()
		},
	}
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// resolveBody reads the finding body from a file or stdin ("-"); empty when no
// source given.
func resolveBody(bodyFile string, stdin io.Reader) (string, error) {
	switch bodyFile {
	case "":
		return "", nil
	case "-":
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", errors.WrapWithDetails(err, "reading body from stdin")
		}
		return strings.TrimSpace(string(data)), nil
	default:
		data, err := os.ReadFile(bodyFile) // #nosec G304 -- path is an explicit user-supplied CLI argument
		if err != nil {
			return "", errors.WrapWithDetails(err, "reading body file", "path", bodyFile)
		}
		return strings.TrimSpace(string(data)), nil
	}
}
