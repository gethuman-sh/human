package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The PR-loop outage route must exist in the written machine, not only in the
// code: a transition the document does not carry is one `human fsm where` cannot
// show and the next planner will not read (SC-5627).
func TestPipelineFSM_PRLoopStepsCanReachSubstrateDown(t *testing.T) {
	doc := loadFSMDoc(t)
	var found bool
	for _, e := range doc.Events {
		if e.Name != "substrate-unreachable" {
			continue
		}
		found = true
		assert.Equal(t, "substrate-down", e.Dst)
		assert.Subset(t, e.Src, []string{"pr-review", "pr-fix", "deploy-fixing"},
			"a recorded outage from either loop step parks the card, it does not red it")
		assert.Contains(t, e.Where, "prLoopOutage",
			"the document must name where the loop's own outage post lives")
	}
	require.True(t, found, "substrate-unreachable must be a declared transition")
}
