package settings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
}
