package cmdping

import (
	"fmt"
	"net"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

const dialTimeout = 2 * time.Second

// BuildPingCmd creates the "ping" command.
func BuildPingCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Check if the daemon is reachable",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			if !cmd.Flags().Changed("addr") {
				if info, err := daemon.ReadInfo(); err == nil && info.Addr != "" {
					addr = info.Addr
				} else {
					// Fallback: try host.docker.internal (inside containers).
					addr = fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultPort)
				}
			}

			if addr == "" {
				_, _ = fmt.Fprintln(out, "No daemon configured")
				return errors.WithDetails("no daemon address found")
			}

			start := time.Now()
			conn, err := net.DialTimeout("tcp", addr, dialTimeout)
			elapsed := time.Since(start)

			if err != nil {
				_, _ = fmt.Fprintf(out, "Daemon at %s is not reachable\n", addr)
				return errors.WrapWithDetails(err, "cannot connect", "addr", addr)
			}
			_ = conn.Close()

			_, _ = fmt.Fprintf(out, "Daemon at %s is reachable (%dms)\n", addr, elapsed.Milliseconds())
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "Daemon address to check (auto-detected from ~/.human/daemon.json)")
	return cmd
}
