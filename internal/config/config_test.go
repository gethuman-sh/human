package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalSection_happyPath(t *testing.T) {
	dir := t.TempDir()
	yaml := `jiras:
  - name: work
    url: https://example.atlassian.net
    user: alice@example.com
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	type entry struct {
		Name string `mapstructure:"name"`
		URL  string `mapstructure:"url"`
		User string `mapstructure:"user"`
	}
	var entries []entry
	err := UnmarshalSection(dir, "jiras", &entries)

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "work", entries[0].Name)
	assert.Equal(t, "https://example.atlassian.net", entries[0].URL)
	assert.Equal(t, "alice@example.com", entries[0].User)
}

func TestUnmarshalSection_missingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()

	var target []map[string]string
	err := UnmarshalSection(dir, "jiras", &target)

	assert.NoError(t, err)
	assert.Nil(t, target)
}

func TestUnmarshalSection_malformedYAMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(":\n  bad: [yaml\n"), 0o644))

	var target map[string]string
	err := UnmarshalSection(dir, "jiras", &target)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config file")
}

func TestReadProjectName_happyPath(t *testing.T) {
	dir := t.TempDir()
	yaml := `project: infra
jiras:
  - name: work
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	name := ReadProjectName(dir)
	assert.Equal(t, "infra", name)
}

func TestReadProjectName_missingField(t *testing.T) {
	dir := t.TempDir()
	yaml := `jiras:
  - name: work
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	name := ReadProjectName(dir)
	assert.Equal(t, "", name)
}

func TestReadProjectName_missingFile(t *testing.T) {
	dir := t.TempDir()

	name := ReadProjectName(dir)
	assert.Equal(t, "", name)
}

func TestHasConfigFile_found(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: infra\n"), 0o644))

	assert.True(t, HasConfigFile(dir))
}

func TestHasConfigFile_missing(t *testing.T) {
	dir := t.TempDir()

	assert.False(t, HasConfigFile(dir))
}

func TestHasConfigFile_altExtension(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig"), []byte("project: infra\n"), 0o644))

	assert.True(t, HasConfigFile(dir))
}

func TestUnmarshalSection_missingSectionReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	yaml := `jiras:
  - name: work
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	type entry struct {
		Name string `mapstructure:"name"`
	}
	var entries []entry
	err := UnmarshalSection(dir, "githubs", &entries)

	require.NoError(t, err)
	assert.Empty(t, entries)
}
