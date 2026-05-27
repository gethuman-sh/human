package cmdutil

import "github.com/gethuman-sh/human/internal/tracker"

func init() {
	tracker.CredSpecs = map[string]tracker.CredSpec{
		"jira": {
			Kind: "jira", EnvPrefix: "JIRA", Label: "Jira",
			Required: []string{"KEY", "USER"},
			HelpURL:  "https://id.atlassian.com/manage-profile/security/api-tokens",
		},
		"github": {
			Kind: "github", EnvPrefix: "GITHUB", Label: "GitHub",
			Required: []string{"TOKEN"},
			HelpURL:  "https://github.com/settings/tokens",
		},
		"gitlab": {
			Kind: "gitlab", EnvPrefix: "GITLAB", Label: "GitLab",
			Required: []string{"TOKEN"},
			HelpURL:  "https://gitlab.com/-/user_settings/personal_access_tokens",
		},
		"linear": {
			Kind: "linear", EnvPrefix: "LINEAR", Label: "Linear",
			Required: []string{"TOKEN"},
			HelpURL:  "https://linear.app/settings/api",
		},
		"azuredevops": {
			Kind: "azuredevops", EnvPrefix: "AZURE", Label: "Azure DevOps",
			Required: []string{"TOKEN"},
			HelpURL:  "https://dev.azure.com/_usersSettings/tokens",
		},
		"shortcut": {
			Kind: "shortcut", EnvPrefix: "SHORTCUT", Label: "Shortcut",
			Required: []string{"TOKEN"},
			HelpURL:  "https://app.shortcut.com/settings/account/api-tokens",
		},
	}
}
