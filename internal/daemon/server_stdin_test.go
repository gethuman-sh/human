package daemon

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// stdinEchoCmd echoes whatever the command reads from stdin, so a test can see
// what the daemon actually handed it.
func stdinEchoCmd() *cobra.Command {
	return &cobra.Command{
		Use: "root",
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

// The daemon runs commands in its own process, so without forwarding, every
// `--body-file -` read the daemon's stdin and silently got nothing: a marker
// posted an empty body, a state write failed validation.
func TestExecuteCommand_ForwardsClientStdin(t *testing.T) {
	s := &Server{CmdFactory: stdinEchoCmd, Logger: zerolog.Nop()}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		s.executeCommand(server, Request{Args: []string{}, Stdin: "{\"exit\":\"done\"}"}, ".")
	}()

	line, err := bufio.NewReader(client).ReadBytes('\n')
	require.NoError(t, err)

	var resp Response
	require.NoError(t, json.Unmarshal(line, &resp))
	require.Equal(t, "{\"exit\":\"done\"}", resp.Stdout,
		"the command must read the CLIENT's stdin, not the daemon's")
}

// A request without stdin must leave the command reading an empty stream
// rather than blocking on the daemon's own descriptor.
func TestExecuteCommand_EmptyStdinIsNotTheDaemons(t *testing.T) {
	s := &Server{CmdFactory: stdinEchoCmd, Logger: zerolog.Nop()}
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go func() {
		defer func() { _ = server.Close() }()
		s.executeCommand(server, Request{Args: []string{}}, ".")
	}()

	line, err := bufio.NewReader(client).ReadBytes('\n')
	require.NoError(t, err)

	var resp Response
	require.NoError(t, json.Unmarshal(line, &resp))
	require.Empty(t, resp.Stdout)
}
