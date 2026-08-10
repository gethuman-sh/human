package devcontainer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMount_StringRendersDockerBindForm(t *testing.T) {
	assert.Equal(t, "/src:/dst", Bind("/src", "/dst").String())
	assert.Equal(t, "/src:/dst:ro", Bind("/src", "/dst").ReadOnly().String())
	assert.Equal(t, "/src:/dst:ro,z", Bind("/src", "/dst", "ro", "z").String())
}

func TestMount_ReadOnlyIsIdempotent(t *testing.T) {
	assert.Equal(t, "/src:/dst:ro", Bind("/src", "/dst").ReadOnly().ReadOnly().String())
}

// ReadOnly must not write through the slice its receiver shares with the value
// it was called on — a mount is passed by value and callers reuse the base.
func TestMount_ReadOnlyDoesNotMutateTheOriginal(t *testing.T) {
	base := Bind("/src", "/dst", "z")
	ro := base.ReadOnly()
	assert.Equal(t, "/src:/dst:z", base.String())
	assert.Equal(t, "/src:/dst:z,ro", ro.String())
}

func TestParseBind(t *testing.T) {
	cases := []struct {
		in     string
		source string
		target string
		opts   []string
	}{
		{"/host:/container", "/host", "/container", nil},
		{"/host:/container:ro", "/host", "/container", []string{"ro"}},
		{"/host:/container:ro,z", "/host", "/container", []string{"ro", "z"}},
		{"named-volume:/data", "named-volume", "/data", nil},
	}
	for _, c := range cases {
		m, ok := ParseBind(c.in)
		require.True(t, ok, c.in)
		assert.Equal(t, c.source, m.Source, c.in)
		assert.Equal(t, c.target, m.Target, c.in)
		assert.Equal(t, c.opts, m.Options, c.in)
	}
}

// A Windows source path is one path, not two fields. Reading `C:\repo` as a
// drive plus a directory made the TARGET of every Windows mount its own source
// directory, so the dedupe compared the wrong thing in both directions.
func TestParseBind_WindowsDriveLetterIsNotASeparator(t *testing.T) {
	m, ok := ParseBind(`C:\repo:/workspace`)
	require.True(t, ok)
	assert.Equal(t, `C:\repo`, m.Source)
	assert.Equal(t, "/workspace", m.Target)

	m, ok = ParseBind(`C:\repo:/workspace:ro`)
	require.True(t, ok)
	assert.Equal(t, `C:\repo`, m.Source)
	assert.Equal(t, "/workspace", m.Target)
	assert.Equal(t, []string{"ro"}, m.Options)
}

func TestParseBind_RefusesHalfAMount(t *testing.T) {
	for _, in := range []string{"", "/only-a-path", "/host:", ":/container"} {
		_, ok := ParseBind(in)
		assert.False(t, ok, in)
	}
}

// The old dedupe skipped anything it could not split into two fields, which
// removed it from the result rather than leaving it alone. A mount is now a
// pair by construction, so there is nothing half-formed to drop.
func TestDedupeMounts_KeepsEveryDistinctTarget(t *testing.T) {
	got := dedupeMounts([]Mount{
		Bind("/a", "/x"),
		Bind(`C:\b`, "/y"),
		Bind("/c", "/x"),
	})
	require.Len(t, got, 2)
	assert.Equal(t, "/y", got[0].Target, "order follows the surviving entries")
	assert.Equal(t, "/c", got[1].Source, "the last mount for a target wins")
}

func TestBindStrings_NilForEmpty(t *testing.T) {
	assert.Nil(t, bindStrings(nil))
	assert.Equal(t, []string{"/a:/x"}, bindStrings([]Mount{Bind("/a", "/x")}))
}
