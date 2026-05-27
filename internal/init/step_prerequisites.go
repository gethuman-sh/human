package init

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// Prerequisite describes a required external tool.
type Prerequisite struct {
	Binary     string // binary name to look up on PATH
	Purpose    string // short description of why it's needed
	InstallURL string // URL with install instructions
}

// PrerequisiteRegistry returns all required prerequisites for human.
func PrerequisiteRegistry() []Prerequisite {
	return []Prerequisite{
		{
			Binary:     "docker",
			Purpose:    "devcontainer and agent container management",
			InstallURL: "https://docs.docker.com/get-docker/",
		},
		{
			Binary:     "tmux",
			Purpose:    "Claude session discovery and management",
			InstallURL: "https://github.com/tmux/tmux/wiki/Installing",
		},
	}
}

// PathLooker abstracts exec.LookPath for testability.
type PathLooker interface {
	LookPath(file string) (string, error)
}

// OSPathLooker implements PathLooker using the real os/exec.LookPath.
type OSPathLooker struct{}

// LookPath delegates to exec.LookPath.
func (OSPathLooker) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

type prerequisitesStep struct {
	looker  PathLooker
	prereqs []Prerequisite
}

// NewPrerequisitesStep creates a WizardStep that checks for required external tools.
func NewPrerequisitesStep(looker PathLooker) WizardStep {
	return &prerequisitesStep{looker: looker, prereqs: PrerequisiteRegistry()}
}

// newPrerequisitesStepWith creates a step with custom prerequisites (for testing).
func newPrerequisitesStepWith(looker PathLooker, prereqs []Prerequisite) WizardStep {
	return &prerequisitesStep{looker: looker, prereqs: prereqs}
}

func (s *prerequisitesStep) Name() string { return "prerequisites" }

func (s *prerequisitesStep) Run(w io.Writer, _ claude.FileWriter) ([]string, error) {
	_, _ = fmt.Fprintln(w, "Checking prerequisites...")

	var missing []Prerequisite
	for _, p := range s.prereqs {
		if _, err := s.looker.LookPath(p.Binary); err != nil {
			missing = append(missing, p)
		} else {
			_, _ = fmt.Fprintf(w, "  ✓ %s\n", p.Binary)
		}
	}

	if len(missing) == 0 {
		_, _ = fmt.Fprintln(w, "All prerequisites satisfied.")
		return nil, nil
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, "Missing prerequisites:")
	for _, p := range missing {
		lines = append(lines, fmt.Sprintf("  ✗ %s — %s", p.Binary, p.Purpose))
		lines = append(lines, fmt.Sprintf("    Install: %s", p.InstallURL))
	}

	msg := strings.Join(lines, "\n")
	_, _ = fmt.Fprintln(w, msg)

	return nil, errors.WithDetails("missing required tools: %s",
		"tools", missingNames(missing))
}

func missingNames(prereqs []Prerequisite) string {
	names := make([]string, len(prereqs))
	for i, p := range prereqs {
		names[i] = p.Binary
	}
	return strings.Join(names, ", ")
}
