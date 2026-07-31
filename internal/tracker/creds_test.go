package tracker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// Populate CredSpecs for tests; in production, cmd/cmdutil/credspecs.go does this.
	CredSpecs = map[string]CredSpec{
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

func TestCredSpecForKind_known(t *testing.T) {
	kinds := []string{"jira", "github", "gitlab", "linear", "azuredevops", "shortcut"}
	for _, kind := range kinds {
		spec, ok := CredSpecForKind(kind)
		require.True(t, ok, "expected spec for kind %s", kind)
		assert.Equal(t, kind, spec.Kind)
		assert.NotEmpty(t, spec.Required)
		assert.NotEmpty(t, spec.Label)
		assert.NotEmpty(t, spec.HelpURL)
	}
}

func TestCredSpecForKind_unknown(t *testing.T) {
	_, ok := CredSpecForKind("unknown")
	assert.False(t, ok)
}

func TestCheckCredsEnv_allSet(t *testing.T) {
	spec := CredSpec{
		Kind: "github", EnvPrefix: "GITHUB", Required: []string{"TOKEN"},
	}

	result := CheckCredsEnv(spec, func(key string) string {
		if key == "GITHUB_TOKEN" {
			return "ghp_test123"
		}
		return ""
	})

	assert.True(t, result.Complete)
	assert.Equal(t, "ghp_test123", result.Available["TOKEN"])
	assert.Empty(t, result.Missing)
}

func TestCheckCredsEnv_partial(t *testing.T) {
	spec := CredSpec{
		Kind: "jira", EnvPrefix: "JIRA", Required: []string{"KEY", "USER"},
	}

	result := CheckCredsEnv(spec, func(key string) string {
		if key == "JIRA_KEY" {
			return "some-key"
		}
		return ""
	})

	assert.False(t, result.Complete)
	assert.Equal(t, "some-key", result.Available["KEY"])
	assert.Equal(t, []string{"USER"}, result.Missing)
}

func TestCheckCredsEnv_noneSet(t *testing.T) {
	spec := CredSpec{
		Kind: "github", EnvPrefix: "GITHUB", Required: []string{"TOKEN"},
	}

	result := CheckCredsEnv(spec, func(_ string) string { return "" })

	assert.False(t, result.Complete)
	assert.Empty(t, result.Available)
	assert.Equal(t, []string{"TOKEN"}, result.Missing)
}

func TestDiagnoseTrackers_withToken(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "linears" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "work"}}
		}
		return nil
	}
	getenv := func(key string) string {
		if key == "LINEAR_WORK_TOKEN" {
			return "lin_test"
		}
		return ""
	}

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "work" && statuses[i].Kind == "linear" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected linear/work in results")
	assert.True(t, found.Working)
	assert.Empty(t, found.Missing)
	assert.Equal(t, "Linear", found.Label)
}

func TestDiagnoseTrackers_missingToken(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "githubs" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "personal"}}
		}
		return nil
	}
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "personal" && statuses[i].Kind == "github" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected github/personal in results")
	assert.False(t, found.Working)
	assert.Contains(t, found.Missing, "GITHUB_TOKEN")
}

func TestDiagnoseTrackers_configField(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "jiras" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "acme", Key: "from-config", User: "alice@acme.com"}}
		}
		return nil
	}
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "acme" && statuses[i].Kind == "jira" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected jira/acme in results")
	assert.True(t, found.Working)
	assert.Empty(t, found.Missing)
}

func TestDiagnoseTrackers_globalEnvOverride(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "linears" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "team"}}
		}
		return nil
	}
	getenv := func(key string) string {
		if key == "LINEAR_TOKEN" {
			return "global-token"
		}
		return ""
	}

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "team" && statuses[i].Kind == "linear" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected linear/team in results")
	assert.True(t, found.Working)
}

func TestDiagnoseTrackers_vaultRef(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "linears" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "work", Token: "1pw://Private/Linear Token/notesPlain"}}
		}
		return nil
	}
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "work" && statuses[i].Kind == "linear" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected linear/work in results")
	assert.False(t, found.Working, "vault ref should not count as working")
	assert.True(t, found.VaultRef, "should be flagged as vault ref")
	assert.Empty(t, found.Missing, "no env vars missing — token is present as vault ref")
}

func TestDiagnoseTrackers_noConfig(t *testing.T) {
	unmarshal := func(_, _ string, _ any) error { return nil }
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)
	assert.Empty(t, statuses)
}

func TestDiagnoseTrackers_sorted(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		switch section {
		case "linears":
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "beta"}, {Name: "alpha"}}
		case "githubs":
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "repo"}}
		}
		return nil
	}
	getenv := func(_ string) string { return "val" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	// Should be sorted by kind then name: github/repo, linear/alpha, linear/beta
	require.Len(t, statuses, 3)
	assert.Equal(t, "github", statuses[0].Kind)
	assert.Equal(t, "repo", statuses[0].Name)
	assert.Equal(t, "linear", statuses[1].Kind)
	assert.Equal(t, "alpha", statuses[1].Name)
	assert.Equal(t, "linear", statuses[2].Kind)
	assert.Equal(t, "beta", statuses[2].Name)
}

func TestFormatMissingCreds(t *testing.T) {
	spec := CredSpec{
		Kind: "jira", EnvPrefix: "JIRA", Label: "Jira",
		Required: []string{"KEY", "USER"},
		HelpURL:  "https://example.com/tokens",
	}
	result := CredResult{
		Spec:    spec,
		Missing: []string{"KEY", "USER"},
	}
	parsed := &ParsedURL{BaseURL: "https://myco.atlassian.net"}

	msg := FormatMissingCreds(result, parsed)

	assert.Contains(t, msg, "Jira")
	assert.Contains(t, msg, "https://myco.atlassian.net")
	assert.Contains(t, msg, "JIRA_KEY")
	assert.Contains(t, msg, "JIRA_USER")
	assert.Contains(t, msg, "https://example.com/tokens")
}

// TestDiagnoseTrackers_skipsForgeOnly locks the SC-1671 rule that a role: forge
// githubs entry is not a tracker: it must not appear in the diagnosis that backs
// `human tracker list` and tracker counting.
func TestDiagnoseTrackers_skipsForgeOnly(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "githubs" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{
				{Name: "prs", Token: "ghp_forge", Role: "forge"},
				{Name: "issues", Token: "ghp_tracker"},
			}
		}
		return nil
	}
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	for _, s := range statuses {
		assert.NotEqual(t, "prs", s.Name, "forge-only entry must be excluded from tracker diagnosis")
	}
	var tracker *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "issues" {
			tracker = &statuses[i]
		}
	}
	require.NotNil(t, tracker, "the tracker-role github entry must still be diagnosed")
}
