package daemon

import (
	"bufio"
	"context"
	"net"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A forwarded command runs inside the daemon, and the one that starts a deploy
// needs the daemon's own engine wiring to launch the reviewer. The server hands
// its resolver over on the command's context; a command run bare finds none.
func TestExecuteCommand_CarriesTheTransitionDeps(t *testing.T) {
	var seen TransitionDepsResolver
	factory := func() *cobra.Command {
		return &cobra.Command{
			Use: "probe",
			RunE: func(cmd *cobra.Command, _ []string) error {
				seen = TransitionDepsFromContext(cmd.Context())
				return nil
			},
		}
	}
	resolver := TransitionDepsResolver(func(string) (BoardTransitionDeps, error) {
		return BoardTransitionDeps{WorkspaceDir: "/daemon"}, nil
	})
	s := &Server{CmdFactory: factory, Logger: zerolog.Nop(), TransitionDeps: resolver}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = server.Close() }()
		s.executeCommand(server, Request{Args: []string{}}, ".")
	}()
	_, err := bufio.NewReader(client).ReadBytes('\n')
	require.NoError(t, err)

	require.NotNil(t, seen, "the forwarded command must find the daemon's resolver on its context")
	deps, err := seen("SC-1")
	require.NoError(t, err)
	assert.Equal(t, "/daemon", deps.WorkspaceDir)

	assert.Nil(t, TransitionDepsFromContext(context.Background()), "a bare run has no daemon wiring")
	assert.Nil(t, TransitionDepsFromContext(WithTransitionDeps(context.Background(), nil)), "a nil resolver is not carried")
}
