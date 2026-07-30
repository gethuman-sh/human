package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProjectRegistry_WithProjectName(t *testing.T) {
	dir := t.TempDir()
	yaml := "project: infra\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	require.Len(t, reg.Entries(), 1)
	assert.Equal(t, "infra", reg.Entries()[0].Name)
	assert.Equal(t, dir, reg.Entries()[0].Dir)
}

func TestNewProjectRegistry_FallsBackToBasename(t *testing.T) {
	dir := t.TempDir()
	// No .humanconfig — name should be directory basename.
	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	require.Len(t, reg.Entries(), 1)
	assert.Equal(t, filepath.Base(dir), reg.Entries()[0].Name)
}

func TestNewProjectRegistry_MultipleProjects(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dirA, ".humanconfig.yaml"), []byte("project: alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, ".humanconfig.yaml"), []byte("project: beta\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dirA, dirB})
	require.NoError(t, err)
	assert.Len(t, reg.Entries(), 2)
	assert.False(t, reg.Single())
}

func TestProjectRegistry_Single(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	assert.True(t, reg.Single())
}

func TestProjectRegistry_Resolve_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: myproj\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)

	entry, ok := reg.Resolve(dir)
	assert.True(t, ok)
	assert.Equal(t, "myproj", entry.Name)
}

func TestProjectRegistry_Resolve_PrefixMatch(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub", "deep")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: myproj\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)

	entry, ok := reg.Resolve(subDir)
	assert.True(t, ok)
	assert.Equal(t, "myproj", entry.Name)
}

func TestProjectRegistry_Resolve_LongestPrefixWins(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(parent, ".humanconfig.yaml"), []byte("project: parent-proj\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(child, ".humanconfig.yaml"), []byte("project: child-proj\n"), 0o644))

	reg, err := NewProjectRegistry([]string{parent, child})
	require.NoError(t, err)

	// cwd inside child should match child (longest prefix)
	deepDir := filepath.Join(child, "src")
	require.NoError(t, os.MkdirAll(deepDir, 0o755))

	entry, ok := reg.Resolve(deepDir)
	assert.True(t, ok)
	assert.Equal(t, "child-proj", entry.Name)

	// cwd directly in parent (but not child) should match parent
	otherDir := filepath.Join(parent, "other")
	require.NoError(t, os.MkdirAll(otherDir, 0o755))

	entry, ok = reg.Resolve(otherDir)
	assert.True(t, ok)
	assert.Equal(t, "parent-proj", entry.Name)
}

func TestProjectRegistry_Resolve_NoMatch(t *testing.T) {
	dir := t.TempDir()
	unrelated := t.TempDir()

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)

	_, ok := reg.Resolve(unrelated)
	assert.False(t, ok)
}

func TestProjectRegistry_Resolve_EmptyCwd_SingleProject(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: solo\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)

	// Empty cwd should fall back to single project.
	entry, ok := reg.Resolve("")
	assert.True(t, ok)
	assert.Equal(t, "solo", entry.Name)
}

func TestProjectRegistry_Resolve_EmptyCwd_MultiProject(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	reg, err := NewProjectRegistry([]string{dirA, dirB})
	require.NoError(t, err)

	// Empty cwd with multiple projects should fail.
	_, ok := reg.Resolve("")
	assert.False(t, ok)
}

func TestProjectRegistry_Resolve_NoFalsePrefix(t *testing.T) {
	// Ensure /home/user/project doesn't match /home/user/project-extra
	dir := t.TempDir()
	extraDir := dir + "-extra"
	require.NoError(t, os.MkdirAll(extraDir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(extraDir) })

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)

	_, ok := reg.Resolve(extraDir)
	assert.False(t, ok)
}

func TestPathHasPrefix(t *testing.T) {
	tests := []struct {
		path   string
		prefix string
		want   bool
	}{
		{"/home/user/project", "/home/user/project", true},
		{"/home/user/project/sub", "/home/user/project", true},
		{"/home/user/project-extra", "/home/user/project", false},
		{"/home/user/proj", "/home/user/project", false},
		{"/other/path", "/home/user/project", false},
	}
	for _, tt := range tests {
		t.Run(tt.path+"_vs_"+tt.prefix, func(t *testing.T) {
			assert.Equal(t, tt.want, pathHasPrefix(tt.path, tt.prefix))
		})
	}
}

func TestProjectEntry_EnvLookup_ProjectScopedOverridesGlobal(t *testing.T) {
	entry := ProjectEntry{Name: "infra", Dir: "/projects/infra"}

	// Set both project-scoped and global env vars.
	t.Setenv("HUMAN_INFRA_GITHUB_TOKEN", "project-token")
	t.Setenv("GITHUB_TOKEN", "global-token")

	lookup := entry.EnvLookup()

	// Project-scoped should win.
	val, ok := lookup("GITHUB_TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "project-token", val)
}

func TestProjectEntry_EnvLookup_FallsBackToGlobal(t *testing.T) {
	entry := ProjectEntry{Name: "infra", Dir: "/projects/infra"}

	// Only global is set, no project-scoped.
	t.Setenv("HUMAN_INFRA_GITHUB_TOKEN", "")
	require.NoError(t, os.Unsetenv("HUMAN_INFRA_GITHUB_TOKEN"))
	t.Setenv("GITHUB_TOKEN", "global-token")

	lookup := entry.EnvLookup()

	val, ok := lookup("GITHUB_TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "global-token", val)
}

func TestProjectEntry_EnvLookup_NotFound(t *testing.T) {
	entry := ProjectEntry{Name: "infra", Dir: "/projects/infra"}

	// Neither project-scoped nor global set.
	t.Setenv("HUMAN_INFRA_GITHUB_TOKEN", "")
	require.NoError(t, os.Unsetenv("HUMAN_INFRA_GITHUB_TOKEN"))
	t.Setenv("GITHUB_TOKEN", "")
	require.NoError(t, os.Unsetenv("GITHUB_TOKEN"))

	lookup := entry.EnvLookup()

	_, ok := lookup("GITHUB_TOKEN")
	assert.False(t, ok)
}

func TestProjectEntry_EnvLookup_PerInstanceScoping(t *testing.T) {
	entry := ProjectEntry{Name: "infra", Dir: "/projects/infra"}

	// Project-scoped per-instance: HUMAN_INFRA_GITHUB_WORK_TOKEN
	t.Setenv("HUMAN_INFRA_GITHUB_WORK_TOKEN", "project-instance-token")
	t.Setenv("GITHUB_WORK_TOKEN", "global-instance-token")

	lookup := entry.EnvLookup()

	val, ok := lookup("GITHUB_WORK_TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "project-instance-token", val)
}

func TestProjectEntry_EnvLookup_NameUppercased(t *testing.T) {
	// Project name with mixed case should be uppercased in env prefix.
	entry := ProjectEntry{Name: "MyApp", Dir: "/projects/myapp"}

	t.Setenv("HUMAN_MYAPP_SHORTCUT_TOKEN", "scoped-tok")
	t.Setenv("SHORTCUT_TOKEN", "global-tok")

	lookup := entry.EnvLookup()

	val, ok := lookup("SHORTCUT_TOKEN")
	assert.True(t, ok)
	assert.Equal(t, "scoped-tok", val)
}

// twoProjectRegistry builds a registry with two distinct registered project
// directories. Entries are length-sorted by NewProjectRegistry, so callers
// must not assume creation order maps to Entries() order.
func twoProjectRegistry(t *testing.T) *ProjectRegistry {
	t.Helper()
	dirA := t.TempDir()
	dirB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dirA, ".humanconfig.yaml"), []byte("project: alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, ".humanconfig.yaml"), []byte("project: beta\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dirA, dirB})
	require.NoError(t, err)
	return reg
}

// oneProjectRegistry builds a registry with a single registered project directory.
func oneProjectRegistry(t *testing.T) *ProjectRegistry {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: solo\n"), 0o644))

	reg, err := NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	return reg
}

func TestProjectRegistry_EntryForKey_routesByRecordedOrigin(t *testing.T) {
	// Two registered projects; SC-2 was served from project B on the last fetch.
	reg := twoProjectRegistry(t)
	a, b := reg.Entries()[0].Dir, reg.Entries()[1].Dir
	reg.SetOrigins([]KeyOrigin{{Key: "SC-1", Dir: a}, {Key: "SC-2", Dir: b}})

	got, err := reg.EntryForKey("SC-2")
	require.NoError(t, err)
	assert.Equal(t, b, got.Dir, "SC-2 must route to the project it was fetched from, not entries[0]")

	got, err = reg.EntryForKey("SC-1")
	require.NoError(t, err)
	assert.Equal(t, a, got.Dir)
}

func TestProjectRegistry_EntryForKey_unknownKeyErrors(t *testing.T) {
	reg := twoProjectRegistry(t)
	_, err := reg.EntryForKey("SC-999")
	require.Error(t, err) // must NOT default to entries[0]
}

func TestProjectRegistry_EntryForKey_ambiguousKeyErrors(t *testing.T) {
	reg := twoProjectRegistry(t)
	a, b := reg.Entries()[0].Dir, reg.Entries()[1].Dir
	reg.SetOrigins([]KeyOrigin{{Key: "ROO-1", Dir: a}, {Key: "ROO-1", Dir: b}})
	_, err := reg.EntryForKey("ROO-1")
	require.Error(t, err) // overlapping key space: refuse to route
}

func TestProjectRegistry_EntryForKey_singleProjectAlwaysResolves(t *testing.T) {
	reg := oneProjectRegistry(t) // single entry; no origins recorded
	got, err := reg.EntryForKey("ANY-1")
	require.NoError(t, err)
	assert.Equal(t, reg.Entries()[0].Dir, got.Dir)
}

func TestProjectRegistry_SoleEntry(t *testing.T) {
	one := oneProjectRegistry(t)
	got, err := one.SoleEntry()
	require.NoError(t, err)
	assert.Equal(t, one.Entries()[0].Dir, got.Dir)

	two := twoProjectRegistry(t)
	_, err = two.SoleEntry()
	require.Error(t, err) // keyless action + multi-project => loud error, no default
}

func TestProjectRegistry_SetOrigins_replacesAtomically(t *testing.T) {
	reg := twoProjectRegistry(t)
	a, b := reg.Entries()[0].Dir, reg.Entries()[1].Dir

	reg.SetOrigins([]KeyOrigin{{Key: "SC-1", Dir: a}})
	got, err := reg.EntryForKey("SC-1")
	require.NoError(t, err)
	assert.Equal(t, a, got.Dir)

	// A fresh fetch replaces the whole index — stale keys must vanish.
	reg.SetOrigins([]KeyOrigin{{Key: "SC-2", Dir: b}})
	_, err = reg.EntryForKey("SC-1")
	require.Error(t, err, "stale origin from a previous fetch must not persist")

	got, err = reg.EntryForKey("SC-2")
	require.NoError(t, err)
	assert.Equal(t, b, got.Dir)
}

func TestProjectEntry_EnvLookup_DifferentProjectsDifferentTokens(t *testing.T) {
	entryA := ProjectEntry{Name: "alpha", Dir: "/projects/alpha"}
	entryB := ProjectEntry{Name: "beta", Dir: "/projects/beta"}

	t.Setenv("HUMAN_ALPHA_GITHUB_TOKEN", "alpha-token")
	t.Setenv("HUMAN_BETA_GITHUB_TOKEN", "beta-token")
	t.Setenv("GITHUB_TOKEN", "global-token")

	lookupA := entryA.EnvLookup()
	lookupB := entryB.EnvLookup()

	valA, okA := lookupA("GITHUB_TOKEN")
	valB, okB := lookupB("GITHUB_TOKEN")

	assert.True(t, okA)
	assert.True(t, okB)
	assert.Equal(t, "alpha-token", valA)
	assert.Equal(t, "beta-token", valB)
}
