package cmdclickup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/clickup"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDeps returns cmdutil.Deps that resolve to a ClickUp client backed by srv.
func testDeps(srv *httptest.Server, teamID string) cmdutil.Deps {
	return cmdutil.Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return []tracker.Instance{
				{
					Name:     "test",
					Kind:     "clickup",
					URL:      srv.URL,
					Provider: clickup.New(srv.URL, "tok-test", teamID),
				},
			}, nil
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
	}
}

// buildTestRoot creates a root command tree with the given ClickUp subcommands.
func buildTestRoot(cmds []*cobra.Command) *cobra.Command {
	root := &cobra.Command{Use: "human", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("tracker", "", "")
	root.PersistentFlags().Bool("safe", false, "")

	clickupCmd := &cobra.Command{Use: "clickup"}
	for _, c := range cmds {
		clickupCmd.AddCommand(c)
	}
	root.AddCommand(clickupCmd)
	return root
}

func TestBuildClickUpCommands_ReturnsExpectedCommands(t *testing.T) {
	cmds := BuildClickUpCommands(cmdutil.Deps{})
	names := make([]string, 0, len(cmds))
	for _, c := range cmds {
		names = append(names, c.Name())
	}
	assert.Contains(t, names, "spaces")
	assert.Contains(t, names, "folders")
	assert.Contains(t, names, "lists")
	assert.Contains(t, names, "members")
	assert.Contains(t, names, "fields")
	assert.Contains(t, names, "field-set")
}

func TestSpacesCmd_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/team/team1/space", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"spaces": [{"id": "sp1", "name": "Engineering"}]}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "team1")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "spaces"})

	err := root.Execute()
	require.NoError(t, err)

	var spaces []clickup.Space
	err = json.Unmarshal(buf.Bytes(), &spaces)
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	assert.Equal(t, "sp1", spaces[0].ID)
	assert.Equal(t, "Engineering", spaces[0].Name)
}

func TestSpacesCmd_table(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"spaces": [{"id": "sp1", "name": "Engineering"}]}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "team1")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "spaces", "--table"})

	err := root.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "ID")
	assert.Contains(t, output, "NAME")
	assert.Contains(t, output, "sp1")
	assert.Contains(t, output, "Engineering")
}

func TestSpacesCmd_noTeamID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	deps := testDeps(srv, "") // no team_id
	root := buildTestRoot(BuildClickUpCommands(deps))
	root.SetArgs([]string{"clickup", "spaces"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team_id")
}

func TestFoldersCmd_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/space/sp1/folder", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"folders": [{"id": "f1", "name": "Sprint Board"}]}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "folders", "--space", "sp1"})

	err := root.Execute()
	require.NoError(t, err)

	var folders []clickup.Folder
	err = json.Unmarshal(buf.Bytes(), &folders)
	require.NoError(t, err)
	require.Len(t, folders, 1)
	assert.Equal(t, "f1", folders[0].ID)
	assert.Equal(t, "Sprint Board", folders[0].Name)
}

func TestListsCmd_byFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/folder/f1/list", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"lists": [{"id": "901", "name": "Sprint 1"}]}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "lists", "--folder", "f1"})

	err := root.Execute()
	require.NoError(t, err)

	var lists []clickup.List
	err = json.Unmarshal(buf.Bytes(), &lists)
	require.NoError(t, err)
	require.Len(t, lists, 1)
	assert.Equal(t, "901", lists[0].ID)
}

func TestListsCmd_bySpace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/space/sp1/list", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"lists": [{"id": "903", "name": "Misc"}]}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "lists", "--space", "sp1"})

	err := root.Execute()
	require.NoError(t, err)

	var lists []clickup.List
	err = json.Unmarshal(buf.Bytes(), &lists)
	require.NoError(t, err)
	require.Len(t, lists, 1)
	assert.Equal(t, "903", lists[0].ID)
}

func TestListsCmd_noFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))
	root.SetArgs([]string{"clickup", "lists"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--folder or --space")
}

func TestMembersCmd_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"teams": [{
				"id": "team1",
				"name": "Workspace",
				"members": [
					{"user": {"id": 100, "username": "alice", "email": "alice@example.com"}}
				]
			}]
		}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "team1")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "members"})

	err := root.Execute()
	require.NoError(t, err)

	var members []clickup.Member
	err = json.Unmarshal(buf.Bytes(), &members)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "alice", members[0].Username)
}

func TestFieldsCmd_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1", r.URL.Path)
		_, _ = fmt.Fprint(w, `{
			"id": "abc1",
			"name": "Test",
			"description": "",
			"status": {"status": "open", "type": "open"},
			"assignees": [],
			"creator": {"id": 100, "username": "alice"},
			"date_created": "1700000000000",
			"date_updated": "1700100000000",
			"url": "https://app.clickup.com/t/abc1",
			"list": {"id": "901", "name": "Sprint 1"},
			"custom_fields": [
				{"id": "cf1", "name": "Points", "type": "number", "value": 5}
			]
		}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "fields", "abc1"})

	err := root.Execute()
	require.NoError(t, err)

	var fields []clickup.CustomFieldValue
	err = json.Unmarshal(buf.Bytes(), &fields)
	require.NoError(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, "cf1", fields[0].ID)
	assert.Equal(t, "Points", fields[0].Name)
}

func TestFieldSetCmd_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/task/abc1/field/cf1", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	deps := testDeps(srv, "")
	root := buildTestRoot(BuildClickUpCommands(deps))

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"clickup", "field-set", "abc1", "cf1", "8"})

	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Set field cf1 on abc1")
}
