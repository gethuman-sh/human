package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NextName returns the next free auto-generated agent name (agent-1,
// agent-2, ...) based on existing agent metadata. Shared by the TUI and
// the GUI dispatch paths so both allocate from the same sequence.
func NextName() string {
	metas, err := ListMetas()
	if err != nil {
		// Metadata unreadable — fall back to a time-based name that cannot
		// collide with the numeric sequence.
		return fmt.Sprintf("agent-%d", time.Now().Unix())
	}
	maxN := 0
	for _, m := range metas {
		if rest, ok := strings.CutPrefix(m.Name, "agent-"); ok {
			if n, parseErr := strconv.Atoi(rest); parseErr == nil && n > maxN {
				maxN = n
			}
		}
	}
	return fmt.Sprintf("agent-%d", maxN+1)
}
