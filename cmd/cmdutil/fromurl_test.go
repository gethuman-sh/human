package cmdutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestInstanceFromURLEnv_github(t *testing.T) {
	parsed := &tracker.ParsedURL{
		Kind:    "github",
		BaseURL: "https://api.github.com",
		Key:     "owner/repo#42",
	}

	check := func(spec tracker.CredSpec) tracker.CredResult {
		return tracker.CredResult{
			Spec:      spec,
			Available: map[string]string{"TOKEN": "ghp_test"},
			Complete:  true,
		}
	}

	inst, ok := instanceFromURLEnv(parsed, check)
	require.True(t, ok)
	assert.Equal(t, "github", inst.Kind)
	assert.Equal(t, "https://api.github.com", inst.URL)
	assert.NotNil(t, inst.Provider)
}

func TestInstanceFromURLEnv_jira(t *testing.T) {
	parsed := &tracker.ParsedURL{
		Kind:    "jira",
		BaseURL: "https://myco.atlassian.net",
		Key:     "HUM-4",
	}

	check := func(spec tracker.CredSpec) tracker.CredResult {
		return tracker.CredResult{
			Spec:      spec,
			Available: map[string]string{"KEY": "api-key", "USER": "alice@example.com"},
			Complete:  true,
		}
	}

	inst, ok := instanceFromURLEnv(parsed, check)
	require.True(t, ok)
	assert.Equal(t, "jira", inst.Kind)
	assert.Equal(t, "https://myco.atlassian.net", inst.URL)
	assert.Equal(t, "alice@example.com", inst.User)
	assert.NotNil(t, inst.Provider)
}

func TestInstanceFromURLEnv_missingCreds(t *testing.T) {
	parsed := &tracker.ParsedURL{
		Kind:    "github",
		BaseURL: "https://api.github.com",
		Key:     "owner/repo#42",
	}

	check := func(spec tracker.CredSpec) tracker.CredResult {
		return tracker.CredResult{
			Spec:     spec,
			Missing:  []string{"TOKEN"},
			Complete: false,
		}
	}

	_, ok := instanceFromURLEnv(parsed, check)
	assert.False(t, ok)
}

func TestInstanceFromURLEnv_unknownKind(t *testing.T) {
	parsed := &tracker.ParsedURL{
		Kind:    "unknown",
		BaseURL: "https://example.com",
		Key:     "123",
	}

	_, ok := instanceFromURLEnv(parsed, func(_ tracker.CredSpec) tracker.CredResult {
		return tracker.CredResult{Complete: true}
	})
	assert.False(t, ok)
}

func TestInstanceFromURLEnv_azuredevops(t *testing.T) {
	parsed := &tracker.ParsedURL{
		Kind:    "azuredevops",
		BaseURL: "https://dev.azure.com",
		Key:     "myproject/42",
		Org:     "myorg",
	}

	check := func(spec tracker.CredSpec) tracker.CredResult {
		return tracker.CredResult{
			Spec:      spec,
			Available: map[string]string{"TOKEN": "azure-pat"},
			Complete:  true,
		}
	}

	inst, ok := instanceFromURLEnv(parsed, check)
	require.True(t, ok)
	assert.Equal(t, "azuredevops", inst.Kind)
	assert.NotNil(t, inst.Provider)
}
