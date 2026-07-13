package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePathScalar(t *testing.T) {
	ref, err := ParsePath("project")
	require.NoError(t, err)
	assert.Equal(t, "project", ref.Group.Section)
	assert.Equal(t, "project", ref.Field.Key)
	assert.Equal(t, -1, ref.Index)
}

func TestParsePathSingleton(t *testing.T) {
	ref, err := ParsePath("vault.provider")
	require.NoError(t, err)
	assert.Equal(t, "vault", ref.Group.Section)
	assert.Equal(t, "provider", ref.Field.Key)
	assert.Empty(t, ref.Instance)
}

func TestParsePathList(t *testing.T) {
	ref, err := ParsePath("linears.work.projects")
	require.NoError(t, err)
	assert.Equal(t, "linears", ref.Group.Section)
	assert.Equal(t, "work", ref.Instance)
	assert.Equal(t, "projects", ref.Field.Key)
	assert.Equal(t, -1, ref.Index)
}

func TestParsePathListInstanceNameWithDots(t *testing.T) {
	// Field keys are a closed set, so everything left of the last valid
	// field segment belongs to the instance name.
	ref, err := ParsePath("linears.my.company.token")
	require.NoError(t, err)
	assert.Equal(t, "my.company", ref.Instance)
	assert.Equal(t, "token", ref.Field.Key)
}

func TestParsePathIndexFallback(t *testing.T) {
	ref, err := ParsePath("linears[1].token")
	require.NoError(t, err)
	assert.Equal(t, 1, ref.Index)
	assert.Empty(t, ref.Instance)
	assert.Equal(t, "token", ref.Field.Key)
}

func TestParsePathErrors(t *testing.T) {
	cases := []string{
		"nosuchsection.foo",
		"vault.nosuchfield",
		"linears.work.nosuchfield",
		"linears",           // list needs instance + field
		"linears..token",    // empty instance
		"project.extra",     // scalar takes no field
		"vault[0].provider", // index on non-list
		"linears[x].token",  // malformed index
		"linears[-1].token", // negative index
	}
	for _, path := range cases {
		_, err := ParsePath(path)
		assert.Error(t, err, "path %q must not parse", path)
	}
}

func TestPathForRoundTrip(t *testing.T) {
	for _, sec := range Registry() {
		for _, g := range sec.Groups {
			for _, f := range g.Fields {
				instance := ""
				if g.IsList {
					instance = "work"
				}
				path := PathFor(g, instance, 0, f.Key)
				ref, err := ParsePath(path)
				require.NoError(t, err, "path %q", path)
				assert.Equal(t, g.Section, ref.Group.Section)
				assert.Equal(t, f.Key, ref.Field.Key)
				assert.Equal(t, instance, ref.Instance)
			}
		}
	}
}

func TestPathForUnnamedUsesIndex(t *testing.T) {
	g, ok := groupBySection("linears")
	require.True(t, ok)
	path := PathFor(*g, "", 2, "token")
	assert.Equal(t, "linears[2].token", path)
	ref, err := ParsePath(path)
	require.NoError(t, err)
	assert.Equal(t, 2, ref.Index)
}
