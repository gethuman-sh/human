package cmdinit

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/charmbracelet/lipgloss"
	"github.com/gethuman-sh/human/errors"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/claude"
	initpkg "github.com/gethuman-sh/human/internal/init"
)

// huhPrompter implements initpkg.Prompter using charmbracelet/huh forms.
type huhPrompter struct{}

func (h huhPrompter) ConfirmAddTrackers() (bool, error) {
	add := true
	err := huh.NewConfirm().
		Title("Add trackers to the config?").
		Affirmative("Yes").
		Negative("No").
		Value(&add).
		Run()
	return add, err
}

func (h huhPrompter) ConfirmOverwrite() (bool, error) {
	var overwrite bool
	err := huh.NewConfirm().
		Title(".humanconfig.yaml already exists. Overwrite?").
		Affirmative("Yes").
		Negative("No").
		Value(&overwrite).
		Run()
	return overwrite, err
}

func (h huhPrompter) SelectServices(available []initpkg.ServiceType) ([]initpkg.ServiceType, error) {
	options := make([]huh.Option[int], len(available))
	for i, svc := range available {
		options[i] = huh.NewOption(svc.Label, i)
	}

	theme := huh.ThemeCharm()
	theme.Focused.SelectedPrefix = lipgloss.NewStyle().SetString("[x] ")
	theme.Focused.UnselectedPrefix = lipgloss.NewStyle().SetString("[ ] ")
	theme.Blurred.SelectedPrefix = theme.Focused.SelectedPrefix
	theme.Blurred.UnselectedPrefix = theme.Focused.UnselectedPrefix

	var indices []int
	ms := huh.NewMultiSelect[int]().
		Title("Select services to configure").
		Description("space/x to toggle, enter to confirm").
		Options(options...).
		Filterable(false).
		Validate(func(selected []int) error {
			if len(selected) == 0 {
				return errors.WithDetails("select at least one service")
			}
			return nil
		}).
		Value(&indices)

	err := huh.NewForm(huh.NewGroup(ms)).
		WithTheme(theme).
		Run()
	if err != nil {
		return nil, err
	}

	selected := make([]initpkg.ServiceType, len(indices))
	for i, idx := range indices {
		selected[i] = available[idx]
	}
	return selected, nil
}

func (h huhPrompter) PromptInstance(svc initpkg.ServiceType) (map[string]string, error) {
	values := map[string]string{}

	var name string
	var url string
	var description string

	fields := []huh.Field{
		huh.NewInput().
			Title(fmt.Sprintf("%s — instance name", svc.Label)).
			Description("a label to identify this instance (e.g. work, personal)").
			Placeholder("work").
			Value(&name),
	}

	if svc.URLRequired || svc.DefaultURL == "" {
		fields = append(fields, huh.NewInput().
			Title("URL").
			Placeholder(svc.DefaultURL).
			Value(&url))
	}

	// Collect extra-field bindings so the values can be written after
	// the form completes instead of via defer-in-loop, which would
	// otherwise race any future pre-return validation step that
	// inspects `values` before the deferred writes run.
	type extraBinding struct {
		field string
		ptr   *string
	}
	var extras []extraBinding
	for _, extra := range svc.ExtraFields {
		val := new(string)
		fields = append(fields, huh.NewInput().
			Title(fmt.Sprintf("%s (required)", extra)).
			Value(val))
		extras = append(extras, extraBinding{field: extra, ptr: val})
	}

	fields = append(fields, huh.NewInput().
		Title("Description (optional)").
		Value(&description))

	form := huh.NewForm(huh.NewGroup(fields...))
	if err := form.Run(); err != nil {
		return nil, err
	}

	if name == "" {
		name = "work"
	}
	values["name"] = name

	if url != "" {
		values["url"] = url
	} else if svc.DefaultURL != "" {
		values["url"] = svc.DefaultURL
	}

	if description != "" {
		values["description"] = description
	}

	for _, b := range extras {
		if *b.ptr != "" {
			values[b.field] = *b.ptr
		}
	}

	return values, nil
}

func (h huhPrompter) ConfirmDevcontainer() (bool, error) {
	create := true
	err := huh.NewConfirm().
		Title("Create devcontainer configuration?").
		Affirmative("Yes").
		Negative("No").
		Value(&create).
		Run()
	return create, err
}

func (h huhPrompter) ConfirmOverwriteDevcontainer() (bool, error) {
	var overwrite bool
	err := huh.NewConfirm().
		Title(".devcontainer/devcontainer.json already exists. Overwrite?").
		Affirmative("Yes").
		Negative("No").
		Value(&overwrite).
		Run()
	return overwrite, err
}

func (h huhPrompter) SelectStacks(available []initpkg.StackType) ([]initpkg.StackType, error) {
	options := make([]huh.Option[int], len(available))
	for i, stack := range available {
		options[i] = huh.NewOption(stack.Label, i)
	}

	// Pre-select fixed stacks (e.g. Node.js required by Claude Code).
	var indices []int
	for i, stack := range available {
		if stack.Fixed {
			indices = append(indices, i)
		}
	}

	theme := huh.ThemeCharm()
	theme.Focused.SelectedPrefix = lipgloss.NewStyle().SetString("[x] ")
	theme.Focused.UnselectedPrefix = lipgloss.NewStyle().SetString("[ ] ")
	theme.Blurred.SelectedPrefix = theme.Focused.SelectedPrefix
	theme.Blurred.UnselectedPrefix = theme.Focused.UnselectedPrefix

	ms := huh.NewMultiSelect[int]().
		Title("Select language stacks for the devcontainer").
		Description("space/x to toggle, enter to confirm (none is fine)").
		Options(options...).
		Filterable(false).
		Value(&indices)

	err := huh.NewForm(huh.NewGroup(ms)).
		WithTheme(theme).
		Run()
	if err != nil {
		return nil, err
	}

	// Ensure fixed stacks are always included.
	selected := make([]initpkg.StackType, 0, len(indices))
	seen := map[int]bool{}
	for _, idx := range indices {
		seen[idx] = true
		selected = append(selected, available[idx])
	}
	for i, stack := range available {
		if stack.Fixed && !seen[i] {
			selected = append(selected, stack)
		}
	}

	return selected, nil
}

func (h huhPrompter) ConfirmProxy() (bool, error) {
	proxy := true
	err := huh.NewConfirm().
		Title("Enable HTTPS proxy (firewall)?").
		Affirmative("Yes").
		Negative("No").
		Value(&proxy).
		Run()
	return proxy, err
}

func (h huhPrompter) ConfirmIntercept() (bool, error) {
	intercept := false
	err := huh.NewConfirm().
		Title("Enable traffic logging (MITM)?").
		Description("Intercept and log API traffic (e.g. Claude Code ↔ Anthropic). Requires CA cert trust.").
		Affirmative("Yes").
		Negative("No").
		Value(&intercept).
		Run()
	return intercept, err
}

func (h huhPrompter) SelectVaultProvider(available []string) (string, error) {
	options := make([]huh.Option[string], 0, len(available)+1)
	options = append(options, huh.NewOption("None", ""))
	for _, p := range available {
		options = append(options, huh.NewOption(p, p))
	}

	var provider string
	err := huh.NewSelect[string]().
		Title("Configure a vault provider for secret resolution?").
		Description("Vault resolves secret references (e.g. 1pw://) in tracker configs").
		Options(options...).
		Value(&provider).
		Run()
	return provider, err
}

func (h huhPrompter) PromptVaultAccount() (string, error) {
	var account string
	err := huh.NewInput().
		Title("1Password account name").
		Description("Top-left in the 1Password app sidebar (leave empty to skip)").
		Value(&account).
		Run()
	return account, err
}

func (h huhPrompter) SelectLspPlugins(available []initpkg.LspPlugin) ([]initpkg.LspPlugin, error) {
	options := make([]huh.Option[int], len(available))
	for i, lsp := range available {
		options[i] = huh.NewOption(lsp.Label, i)
	}

	theme := huh.ThemeCharm()
	theme.Focused.SelectedPrefix = lipgloss.NewStyle().SetString("[x] ")
	theme.Focused.UnselectedPrefix = lipgloss.NewStyle().SetString("[ ] ")
	theme.Blurred.SelectedPrefix = theme.Focused.SelectedPrefix
	theme.Blurred.UnselectedPrefix = theme.Focused.UnselectedPrefix

	var indices []int
	ms := huh.NewMultiSelect[int]().
		Title("Select LSP servers to enable").
		Description("space/x to toggle, enter to confirm").
		Options(options...).
		Filterable(false).
		Value(&indices)

	err := huh.NewForm(huh.NewGroup(ms)).
		WithTheme(theme).
		Run()
	if err != nil {
		return nil, err
	}

	selected := make([]initpkg.LspPlugin, len(indices))
	for i, idx := range indices {
		selected[i] = available[idx]
	}
	return selected, nil
}

func (h huhPrompter) ConfirmMigrateClaude() (bool, error) {
	migrate := true
	err := huh.NewConfirm().
		Title("Do you want to migrate/copy the current Claude setup?").
		Description("Copies session history so claude --continue works in the container").
		Affirmative("Yes").
		Negative("No").
		Value(&migrate).
		Run()
	return migrate, err
}

func (h huhPrompter) PromptContainerPath(detected string) (string, error) {
	var path string
	err := huh.NewInput().
		Title("Container project path").
		Description("Where will this project be mounted inside the container?").
		Placeholder(detected).
		Value(&path).
		Run()
	if err != nil {
		return "", err
	}
	if path == "" {
		return detected, nil
	}
	return path, nil
}

func (h huhPrompter) ConfirmAgentInstall() (bool, error) {
	install := true
	err := huh.NewConfirm().
		Title("Install Claude Code agent integration?").
		Affirmative("Yes").
		Negative("No").
		Value(&install).
		Run()
	return install, err
}

// BuildInitCmd creates the "init" command.
func BuildInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactive setup wizard for .humanconfig.yaml",
		Long: `Interactively configure trackers and tools, write .humanconfig.yaml,
and optionally install Claude Code agent integration.

Credentials are never stored in the config file — the wizard prints
the environment variables you need to set.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state := &initpkg.WizardState{}
			steps := []initpkg.WizardStep{
				initpkg.NewPrerequisitesStep(initpkg.OSPathLooker{}),
				initpkg.NewServicesStep(huhPrompter{}),
				initpkg.NewVaultStep(huhPrompter{}, state),
				initpkg.NewDevcontainerStep(huhPrompter{}, state),
				initpkg.NewProjectConfigStep(state),
				initpkg.NewClaudeMigrateStep(huhPrompter{}),
				initpkg.NewLspSetupStep(huhPrompter{}, initpkg.OSLspInstaller{}, state),
				initpkg.NewAgentInstallStep(huhPrompter{}),
			}
			return initpkg.RunInit(cmd.OutOrStdout(), steps, claude.OSFileWriter{})
		},
	}
}
