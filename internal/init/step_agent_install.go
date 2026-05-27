package init

import (
	"fmt"
	"io"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// AgentInstallPrompter abstracts TUI interactions for the agent install step.
type AgentInstallPrompter interface {
	ConfirmAgentInstall() (bool, error)
}

type agentInstallStep struct {
	prompter AgentInstallPrompter
}

// NewAgentInstallStep creates a WizardStep that optionally installs Claude Code integration.
func NewAgentInstallStep(p AgentInstallPrompter) WizardStep {
	return &agentInstallStep{prompter: p}
}

func (s *agentInstallStep) Name() string { return "agent-install" }

func (s *agentInstallStep) Run(w io.Writer, fw claude.FileWriter) ([]string, error) {
	installAgents, err := s.prompter.ConfirmAgentInstall()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "confirming agent install")
	}
	if installAgents {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Installing Claude Code integration...")
		if err := claude.Install(w, fw, false); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
