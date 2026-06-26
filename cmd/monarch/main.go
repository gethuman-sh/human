// Command monarch is the standalone team-level observability console for a
// swarm of human daemons. Daemons opt in (via the daemon's --monarch flag) and
// stream identity-free telemetry to it over TCP; monarch persists the events to
// SQLite and renders a live work-board + burn TUI. It is a separate binary from
// `human` on purpose: a team runs one monarch (e.g. for a lead/CTO) while each
// developer runs their own daemon.
package main

import (
	"fmt"
	"os"

	"github.com/gethuman-sh/human/cmd/cmdmonarch"
)

// Build-time version metadata, injected via -ldflags (see the Makefile),
// mirroring the human binary's version vars.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd := cmdmonarch.BuildMonarchCmd()
	cmd.Version = fmt.Sprintf("%s (%s) %s", version, commit, date)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
