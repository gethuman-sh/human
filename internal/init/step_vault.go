package init

import (
	"fmt"
	"io"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// VaultPrompter abstracts TUI interactions for the vault setup step.
type VaultPrompter interface {
	SelectVaultProvider(available []string) (string, error)
	PromptVaultAccount() (string, error)
}

type vaultStep struct {
	prompter VaultPrompter
	state    *WizardState
}

// NewVaultStep creates a WizardStep that optionally configures a vault provider.
func NewVaultStep(p VaultPrompter, state *WizardState) WizardStep {
	return &vaultStep{prompter: p, state: state}
}

func (s *vaultStep) Name() string { return "vault" }

func (s *vaultStep) Run(w io.Writer, _ claude.FileWriter) ([]string, error) {
	provider, err := s.prompter.SelectVaultProvider([]string{"1password"})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "selecting vault provider")
	}
	if provider == "" {
		return nil, nil
	}

	s.state.VaultProvider = provider

	account, err := s.prompter.PromptVaultAccount()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "prompting vault account")
	}
	s.state.VaultAccount = account

	_, _ = fmt.Fprintf(w, "Vault: %s", provider)
	if account != "" {
		_, _ = fmt.Fprintf(w, " (account: %s)", account)
	}
	_, _ = fmt.Fprintln(w)

	return nil, nil
}
