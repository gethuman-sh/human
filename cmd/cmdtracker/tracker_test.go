package cmdtracker

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// --- helpers ---

func sampleInstances() []tracker.Instance {
	return []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com", User: "alice", Description: "Work JIRA"},
		{Name: "oss", Kind: "github", URL: "https://github.com/org/repo", User: "", Description: "Open source"},
	}
}

func loaderOK(instances []tracker.Instance) func(string) ([]tracker.Instance, error) {
	return func(_ string) ([]tracker.Instance, error) {
		return instances, nil
	}
}

func loaderErr(err error) func(string) ([]tracker.Instance, error) {
	return func(_ string) ([]tracker.Instance, error) {
		return nil, err
	}
}

func findSubcommand(parent *cobra.Command, use string) *cobra.Command {
	for _, sub := range parent.Commands() {
		if sub.Use == use {
			return sub
		}
	}
	return nil
}

// --- RunTrackerList tests ---

func TestRunTrackerList_JSON(t *testing.T) {
	instances := sampleInstances()
	var buf bytes.Buffer

	err := RunTrackerList(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "work"`)
	assert.Contains(t, out, `"type": "jira"`)
	assert.Contains(t, out, `"url": "https://jira.example.com"`)
	assert.Contains(t, out, `"user": "alice"`)
	assert.Contains(t, out, `"description": "Work JIRA"`)
	assert.Contains(t, out, `"name": "oss"`)
	assert.Contains(t, out, `"type": "github"`)
	assert.Contains(t, out, "// Configured issue trackers")
}

func TestRunTrackerList_Table(t *testing.T) {
	instances := sampleInstances()
	var buf bytes.Buffer

	err := RunTrackerList(&buf, ".", true, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "URL")
	assert.Contains(t, out, "USER")
	assert.Contains(t, out, "DESCRIPTION")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "oss")
	assert.Contains(t, out, "github")
}

func TestRunTrackerList_LoaderError(t *testing.T) {
	loadErr := errors.WithDetails("config not found", "path", ".humanconfig")
	var buf bytes.Buffer

	err := RunTrackerList(&buf, ".", false, loaderErr(loadErr))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not found")
}

func TestRunTrackerList_EmptyDir(t *testing.T) {
	var buf bytes.Buffer
	called := false
	loader := func(dir string) ([]tracker.Instance, error) {
		called = true
		assert.Equal(t, ".", dir)
		return nil, nil
	}

	err := RunTrackerList(&buf, "", false, loader)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRunTrackerList_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := RunTrackerList(&buf, ".", false, loaderOK(nil))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "// Configured issue trackers")
}

func TestRunTrackerList_TableEmpty(t *testing.T) {
	var buf bytes.Buffer
	err := RunTrackerList(&buf, ".", true, loaderOK(nil))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No trackers configured in .humanconfig")
}

// --- PrintTrackerJSON tests ---

func TestPrintTrackerJSON_Entries(t *testing.T) {
	entries := []TrackerEntry{
		{Name: "mytracker", Type: "linear", URL: "https://linear.app", User: "bob", Description: "Linear"},
	}
	var buf bytes.Buffer
	err := PrintTrackerJSON(&buf, entries)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "mytracker"`)
	assert.Contains(t, out, `"type": "linear"`)
}

func TestPrintTrackerJSON_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := PrintTrackerJSON(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "// Configured issue trackers")
}

// --- PrintTrackerTable tests ---

func TestPrintTrackerTable_Entries(t *testing.T) {
	entries := []TrackerEntry{
		{Name: "work", Type: "jira", URL: "https://jira.example.com", User: "alice", Description: "Work"},
		{Name: "oss", Type: "github", URL: "https://github.com", User: "", Description: "OSS"},
	}
	var buf bytes.Buffer
	err := PrintTrackerTable(&buf, entries)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "oss")
	assert.Contains(t, out, "github")
}

func TestPrintTrackerTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := PrintTrackerTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No trackers configured in .humanconfig")
}

func TestPrintTrackerTable_EmptySlice(t *testing.T) {
	var buf bytes.Buffer
	err := PrintTrackerTable(&buf, []TrackerEntry{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No trackers configured in .humanconfig")
}

// --- PrintFindJSON tests ---

func TestPrintFindJSON(t *testing.T) {
	entry := FindResultEntry{Provider: "jira", Project: "KAN", Key: "KAN-123"}
	var buf bytes.Buffer
	err := PrintFindJSON(&buf, entry)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"provider": "jira"`)
	assert.Contains(t, out, `"project": "KAN"`)
	assert.Contains(t, out, `"key": "KAN-123"`)
}

// --- PrintFindTable tests ---

func TestPrintFindTable(t *testing.T) {
	entry := FindResultEntry{Provider: "github", Project: "org/repo", Key: "42"}
	var buf bytes.Buffer
	err := PrintFindTable(&buf, entry)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PROVIDER")
	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "KEY")
	assert.Contains(t, out, "github")
	assert.Contains(t, out, "org/repo")
	assert.Contains(t, out, "42")
}

// --- RunTrackerFind tests ---

func TestRunTrackerFind_LoaderError(t *testing.T) {
	loadErr := errors.WithDetails("load failed", "reason", "missing config")
	var buf bytes.Buffer

	err := RunTrackerFind(context.Background(), &buf, ".", "KAN-1", false, loaderErr(loadErr))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load failed")
}

func TestRunTrackerFind_EmptyDir(t *testing.T) {
	called := false
	loader := func(dir string) ([]tracker.Instance, error) {
		called = true
		assert.Equal(t, ".", dir)
		return nil, errors.WithDetails("no config", "dir", dir)
	}

	var buf bytes.Buffer
	_ = RunTrackerFind(context.Background(), &buf, "", "KEY-1", false, loader)
	assert.True(t, called)
}

// --- RunTrackerFindWithInstances tests ---

func TestRunTrackerFindWithInstances_UnrecognizedKey(t *testing.T) {
	instances := sampleInstances()
	var buf bytes.Buffer

	err := RunTrackerFindWithInstances(context.Background(), &buf, "!!!invalid!!!", instances, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key format")
}

func TestRunTrackerFindWithInstances_NoMatchingTracker(t *testing.T) {
	// shortcut only matches numeric keys, so KAN-123 (jira/linear pattern) won't match
	instances := []tracker.Instance{
		{Name: "myshortcut", Kind: "shortcut", URL: "https://shortcut.com"},
	}
	var buf bytes.Buffer

	err := RunTrackerFindWithInstances(context.Background(), &buf, "KAN-123", instances, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker matches")
}

func TestRunTrackerFindWithInstances_MatchJSON(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com"},
	}
	var buf bytes.Buffer

	err := RunTrackerFindWithInstances(context.Background(), &buf, "KAN-123", instances, false)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"provider": "jira"`)
	assert.Contains(t, out, `"key": "KAN-123"`)
}

func TestRunTrackerFindWithInstances_MatchTable(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com"},
	}
	var buf bytes.Buffer

	err := RunTrackerFindWithInstances(context.Background(), &buf, "KAN-123", instances, true)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PROVIDER")
	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "KEY")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "KAN-123")
}

// --- BuildTrackerCmd tests ---

func TestBuildTrackerCmd_HasSubcommands(t *testing.T) {
	cmd := BuildTrackerCmd(loaderOK(nil))
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["list"], "expected 'list' subcommand")
	assert.True(t, subNames["find KEY"], "expected 'find KEY' subcommand")
}

func TestBuildTrackerCmd_ListHasTableFlag(t *testing.T) {
	cmd := BuildTrackerCmd(loaderOK(nil))
	listCmd := findSubcommand(cmd, "list")
	require.NotNil(t, listCmd)
	f := listCmd.Flags().Lookup("table")
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
}

func TestBuildTrackerCmd_FindHasTableFlag(t *testing.T) {
	cmd := BuildTrackerCmd(loaderOK(nil))
	fc := findSubcommand(cmd, "find KEY")
	require.NotNil(t, fc)
	f := fc.Flags().Lookup("table")
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
}

func TestBuildTrackerCmd_ListRunsJSON(t *testing.T) {
	instances := sampleInstances()
	cmd := BuildTrackerCmd(loaderOK(instances))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "work"`)
	assert.Contains(t, out, `"type": "jira"`)
}

func TestBuildTrackerCmd_ListRunsTable(t *testing.T) {
	instances := sampleInstances()
	cmd := BuildTrackerCmd(loaderOK(instances))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"list", "--table"})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "work")
}

func TestBuildTrackerCmd_ListLoaderError(t *testing.T) {
	loadErr := errors.WithDetails("broken config", "file", ".humanconfig")
	cmd := BuildTrackerCmd(loaderErr(loadErr))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken config")
}

func TestBuildTrackerCmd_FindRequiresArg(t *testing.T) {
	cmd := BuildTrackerCmd(loaderOK(nil))
	cmd.SetArgs([]string{"find"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestBuildTrackerCmd_FindRunsJSON(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com"},
	}
	cmd := BuildTrackerCmd(loaderOK(instances))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"find", "KAN-123"})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"provider": "jira"`)
	assert.Contains(t, out, `"key": "KAN-123"`)
}

func TestBuildTrackerCmd_FindRunsTable(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "work", Kind: "jira", URL: "https://jira.example.com"},
	}
	cmd := BuildTrackerCmd(loaderOK(instances))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"find", "--table", "KAN-123"})

	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PROVIDER")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "KAN-123")
}

func TestBuildTrackerCmd_FindLoaderError(t *testing.T) {
	loadErr := errors.WithDetails("cannot load", "reason", "missing file")
	cmd := BuildTrackerCmd(loaderErr(loadErr))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"find", "KAN-1"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot load")
}

// --- Inferred role ---

func TestRunTrackerList_InferredRole(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "pm", Kind: "shortcut", URL: "https://shortcut.com"},
		{Name: "bare", Kind: "linear", URL: "https://linear.app"},
		{Name: "eng", Kind: "linear", URL: "https://linear.app", Role: "engineering"},
		{Name: "custom", Kind: "jira", URL: "https://jira.example.com", Role: "pm"},
	}
	var buf bytes.Buffer
	err := RunTrackerList(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	// Shortcut still infers "pm" and Jira carries an explicit "pm"; a role-less
	// Linear tracker no longer infers "engineering" ([SC-254]) — the engineering
	// role appears only for the tracker that sets it explicitly.
	assert.Contains(t, out, `"role": "pm"`)
	assert.Contains(t, out, `"role": "engineering"`)
	// The bare Linear tracker must not carry an inferred role.
	assert.Regexp(t, `"name": "bare",[\s\S]*?"type": "linear",[\s\S]*?"description"`, out)
}

// --- Multiple instances mapping ---

func TestRunTrackerList_MapsInstanceFields(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "n1", Kind: "k1", URL: "u1", User: "usr1", Description: "d1"},
	}
	var buf bytes.Buffer
	err := RunTrackerList(&buf, "/some/dir", false, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "n1"`)
	assert.Contains(t, out, `"type": "k1"`)
	assert.Contains(t, out, `"url": "u1"`)
	assert.Contains(t, out, `"user": "usr1"`)
	assert.Contains(t, out, `"description": "d1"`)
}

// --- Topology ---

func TestRunTrackerTopology_SplitJSON(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "board", Kind: "shortcut"},
		{Name: "eng", Kind: "linear", Role: "engineering"},
	}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"topology": "split"`)
	assert.Contains(t, out, `"name": "board"`)
	assert.Contains(t, out, `"name": "eng"`)
}

func TestRunTrackerTopology_SingleOmitsEngineering(t *testing.T) {
	instances := []tracker.Instance{{Name: "board", Kind: "shortcut"}}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"topology": "single"`)
	assert.NotContains(t, out, `"engineering"`)
}

// 1087: the pm entry carries its first configured project so an autonomous
// findbugs sweep can pass --project to `issue create` without a prompt.
func TestRunTrackerTopology_PMProject(t *testing.T) {
	instances := []tracker.Instance{{Name: "board", Kind: "shortcut", Projects: []string{"team-a", "team-b"}}}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"project": "team-a"`)
}

// 1087: with no configured project the field is omitted (omitempty), keeping the
// topology contract backward compatible.
func TestRunTrackerTopology_NoProjectOmitted(t *testing.T) {
	instances := []tracker.Instance{{Name: "board", Kind: "shortcut"}}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), `"project"`)
}

// 1087: `tracker list` output stays byte-for-byte unchanged — the Project field
// is populated only at the topology construction site, never for the list.
func TestRunTrackerList_OmitsProjectField(t *testing.T) {
	instances := []tracker.Instance{{Name: "board", Kind: "shortcut", Projects: []string{"team-a"}}}
	var buf bytes.Buffer
	err := RunTrackerList(&buf, ".", false, loaderOK(instances))
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), `"project"`)
}

func TestRunTrackerTopology_Table(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "board", Kind: "shortcut"},
		{Name: "eng", Kind: "linear", Role: "engineering"},
	}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", true, loaderOK(instances))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "TOPOLOGY")
	assert.Contains(t, out, "split")
	assert.Contains(t, out, "board (shortcut)")
	assert.Contains(t, out, "eng (linear)")
}

func TestRunTrackerTopology_TableAmbiguousPM(t *testing.T) {
	instances := []tracker.Instance{
		{Name: "a", Kind: "jira"},
		{Name: "b", Kind: "linear"},
	}
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", true, loaderOK(instances))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "ambiguous")
}

func TestRunTrackerTopology_LoaderError(t *testing.T) {
	var buf bytes.Buffer
	err := RunTrackerTopology(&buf, ".", false, loaderErr(errors.WithDetails("load failed", "reason", "boom")))
	require.Error(t, err)
}
