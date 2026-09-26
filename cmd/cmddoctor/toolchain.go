package cmddoctor

import (
	"context"
	"errors" // std errors, for Is
	"io"

	"github.com/spf13/cobra"

	humanerrors "github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/gotoolchain"
)

// buildToolchainCmd creates `human doctor toolchain`. It runs where it is
// invoked — the container's own bootstrap — and needs no daemon: the question is
// about THIS filesystem's go.mod and THIS PATH's go, which a daemon on the host
// cannot answer. `doctor` is in main.localSubcommands, so it is never forwarded.
func buildToolchainCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "toolchain",
		Short: "Check that the Go toolchain here satisfies the project's go.mod",
		Long: "Compares the version go.mod requires with the toolchain actually installed and\n" +
			"fails naming both. Meant for a container's postStartCommand: a mismatch must be\n" +
			"said once at container start, not rediscovered by every go invocation in a run.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runToolchainCheck(cmd.OutOrStdout(), dir, gotoolchain.OSProber{})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "project directory holding go.mod")
	return cmd
}

// checkName is the label the toolchain line carries in doctor output.
const checkName = "go toolchain"

// runToolchainCheck prints one doctor line and returns an error only for a
// mismatch it is sure of. Everything unaskable — no go.mod, no go on PATH, an
// unparseable version — passes: this runs in every container's bootstrap, and a
// check that reds a container it cannot judge would be worse than the bug.
func runToolchainCheck(out io.Writer, dir string, prober gotoolchain.Prober) error {
	required, ok, err := gotoolchain.Requirement(dir)
	if err != nil {
		return err
	}
	if !ok {
		pass(out, "no go.mod in "+dir+" — nothing to check")
		return nil
	}
	installed, err := prober.Installed(context.Background())
	if err != nil {
		pass(out, "go.mod requires go "+required+"; the installed toolchain could not be asked: "+err.Error())
		return nil
	}
	checkErr := gotoolchain.Check(required, installed)
	switch {
	case checkErr == nil:
		pass(out, "go "+installed+" satisfies go.mod's go "+required)
		return nil
	case errors.Is(checkErr, gotoolchain.ErrUndecidable):
		pass(out, "go.mod requires go "+required+", installed go "+installed+" — not comparable, left alone")
		return nil
	default:
		detail := checkErr.Error() + " — pin \"version\": \"" + required +
			"\" on the Go devcontainer feature and rebuild the image"
		printCheck(out, daemon.DoctorCheck{Name: checkName, OK: false, Severity: daemon.SeverityBlocking}, detail)
		return humanerrors.WrapWithDetails(checkErr, "the Go toolchain here does not satisfy go.mod",
			"required", required, "installed", installed)
	}
}

func pass(out io.Writer, detail string) {
	printCheck(out, daemon.DoctorCheck{Name: checkName, OK: true}, detail)
}
