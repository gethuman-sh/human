package settings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrySectionKeysUnique(t *testing.T) {
	seenSections := map[string]bool{}
	seenGroups := map[string]bool{}
	for _, sec := range Registry() {
		assert.False(t, seenSections[sec.Key], "duplicate section key %s", sec.Key)
		seenSections[sec.Key] = true
		for _, g := range sec.Groups {
			assert.False(t, seenGroups[g.Section], "duplicate group section %s", g.Section)
			seenGroups[g.Section] = true
		}
	}
}

func TestRegistryFieldKeysUniquePerGroup(t *testing.T) {
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			seen := map[string]bool{}
			for _, f := range g.Fields {
				assert.False(t, seen[f.Key], "duplicate field %s.%s", g.Section, f.Key)
				seen[f.Key] = true
				assert.NotEmpty(t, f.Label, "field %s.%s needs a label", g.Section, f.Key)
			}
		}
	}
}

func TestRegistryCredentialFieldsAreSecret(t *testing.T) {
	// Every field whose key names a credential must be write-only, or a
	// config-get would leak it.
	credentialKeys := map[string]bool{"token": true, "key": true, "secret": true}
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			for _, f := range g.Fields {
				if credentialKeys[f.Key] {
					assert.Equal(t, TypeSecret, f.Type, "%s.%s must be secret", g.Section, f.Key)
				}
			}
		}
	}
}

func TestRegistryRestartRequiredFlags(t *testing.T) {
	// Vault and project feed daemon-startup state; everything else re-reads
	// from disk per request and must NOT carry the restart badge.
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			for _, f := range g.Fields {
				restartExpected := g.Section == "vault" || g.Section == "project"
				assert.Equal(t, restartExpected, f.RestartRequired, "%s.%s restart flag", g.Section, f.Key)
			}
		}
	}
}

func TestRegistryEnumFieldsHaveValues(t *testing.T) {
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			for _, f := range g.Fields {
				if f.Type == TypeEnum {
					assert.NotEmpty(t, f.Enum, "%s.%s enum values", g.Section, f.Key)
				}
				if f.Type != TypeEnum {
					assert.Empty(t, f.Enum, "%s.%s must not carry enum values", g.Section, f.Key)
				}
			}
		}
	}
}

func TestRegistryListGroupsHaveNameField(t *testing.T) {
	// Name-based addressing requires every list group to expose "name".
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			if !g.IsList {
				continue
			}
			_, ok := g.FieldByKey("name")
			assert.True(t, ok, "list group %s needs a name field", g.Section)
		}
	}
}

func TestRegistryTrackerSectionsCoverKnownProviders(t *testing.T) {
	var trackerSections []string
	for _, sec := range Registry() {
		if sec.Key != "trackers" {
			continue
		}
		for _, g := range sec.Groups {
			trackerSections = append(trackerSections, g.Section)
		}
	}
	joined := strings.Join(trackerSections, ",")
	for _, want := range []string{"jiras", "githubs", "gitlabs", "linears", "shortcuts", "azuredevops", "clickups"} {
		assert.Contains(t, joined, want)
	}
	assert.NotContains(t, joined, "forges",
		"a forge holds no issues — filing it under Trackers restates the confusion the section exists to end")
}

// The settings screen was the one place a configured forge could not be seen:
// the schema had no forges group, so `human pr create`'s backend was invisible
// to the only interface a board-first user has (SC-3871).
func TestRegistryHasAForgesSection(t *testing.T) {
	var forgeGroups []Group
	for _, sec := range Registry() {
		if sec.Key == "forges" {
			forgeGroups = sec.Groups
		}
	}
	require.Len(t, forgeGroups, 1, "the forges: section must be editable like every other backend")
	assert.Equal(t, "forges", forgeGroups[0].Section)
	assert.True(t, forgeGroups[0].IsList)

	for _, absent := range []string{"projects", "role", "create_in"} {
		_, ok := forgeGroups[0].FieldByKey(absent)
		assert.False(t, ok, "a forge carries no %s — it holds no issues", absent)
	}
	token, ok := forgeGroups[0].FieldByKey("token")
	require.True(t, ok)
	assert.Equal(t, TypeSecret, token.Type)
}

// The forge role is gone from every tracker section: a githubs: entry is an
// issue tracker, and a code host is a forges: entry ([SC-3876]).
func TestRegistryOffersNoForgeRoleOnAnyTracker(t *testing.T) {
	for _, sec := range Registry() {
		if sec.Key != "trackers" {
			continue
		}
		for _, g := range sec.Groups {
			role, ok := g.FieldByKey("role")
			if !ok {
				continue
			}
			assert.NotContains(t, role.Enum, "forge",
				"%s configures a tracker; a code host is configured under forges:", g.Section)
		}
	}
}
