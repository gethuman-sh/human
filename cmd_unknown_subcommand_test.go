package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole tree, not a list of groups: a group added later would not be on a
// list, and the hole this closes is exactly the one nobody remembered to check.
func TestUnknownSubcommand_EveryGroupRejectsOne(t *testing.T) {
	groups := commandGroups(newRootCmd())
	require.NotEmpty(t, groups, "no command group was found — the walk proves nothing")

	for _, group := range groups {
		args := append(group, "definitely-not-a-subcommand")

		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)

		assert.Error(t, root.Execute(), "`human %s` is accepted as a success", strings.Join(args, " "))
	}
}

// End to end through Execute, because ValidateArgs passing is not the same as
// the process reporting a failure: the bug was a non-zero-looking run that
// exited 0.
func TestUnknownSubcommand_ExecuteFails(t *testing.T) {
	for _, args := range [][]string{
		{"fsm", "next", "stopped"},
		{"marker", "bogus"},
		{"codenav", "bogus"},
		{"bogus"},
	} {
		root := newRootCmd()
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(args)

		err := root.Execute()
		require.Error(t, err, "human %s exited without an error", strings.Join(args, " "))
		assert.Contains(t, err.Error(), "unknown command",
			"human %s failed, but not by naming the unknown command", strings.Join(args, " "))
	}
}

// The group's own help must keep working — it is how a caller recovers from the
// error above, and turning that into an error too would trade one bad answer
// for another.
func TestUnknownSubcommand_BareGroupStillPrintsHelp(t *testing.T) {
	for _, group := range []string{"fsm", "marker", "codenav"} {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{group})

		require.NoError(t, root.Execute(), "human %s", group)
		assert.Contains(t, out.String(), "Available Commands:", "human %s printed no help", group)
	}
}

// A group that decides its own Args keeps that decision; the walk must not
// overwrite one.
func TestRejectUnknownSubcommands_LeavesADeclaredArgsAlone(t *testing.T) {
	parent := &cobra.Command{Use: "parent", Args: cobra.ArbitraryArgs}
	parent.AddCommand(&cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}})

	rejectUnknownSubcommands(parent)

	assert.NoError(t, parent.ValidateArgs([]string{"anything"}))
}

// A runnable command's arguments are its own input, not subcommand names.
func TestRejectUnknownSubcommands_LeavesARunnableParentAlone(t *testing.T) {
	parent := &cobra.Command{Use: "parent", RunE: func(*cobra.Command, []string) error { return nil }}
	parent.AddCommand(&cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}})

	rejectUnknownSubcommands(parent)

	assert.Nil(t, parent.Args, "a runnable parent must keep cobra's default argument handling")
}

// commandGroups returns the argument path of every command that holds
// subcommands, root's own empty path included: root was already the only group
// that rejected an unknown word, and it has to keep doing so.
func commandGroups(root *cobra.Command) [][]string {
	var out [][]string
	var walk func(*cobra.Command, []string)
	walk = func(cmd *cobra.Command, path []string) {
		if cmd.HasSubCommands() {
			out = append(out, path)
		}
		for _, child := range cmd.Commands() {
			walk(child, append(append([]string{}, path...), child.Name()))
		}
	}
	walk(root, nil)
	return out
}
