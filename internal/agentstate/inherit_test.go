package agentstate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A displaced predecessor's inheritance is handed to the successor as "what it
// had already worked out". While several pipeline steps shared one lease name,
// that predecessor could be a different role entirely, and its records would be
// read as the successor's own prior work. Stale rows from that era outlive the
// naming fix, so the scoping is the durable guard.
func TestLeaseInheritsOnlyItsOwnStageRecords(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)
	prev := Meta{Agent: "board-SC-2405-prreview"}

	// A PR reviewer that held the shared "fix" lease and left its records behind.
	for _, kv := range [][2]string{
		{"stage.pr-review", "reviewer findings"},
		{"fix.evidence", "same-stage evidence"},
		{"capabilities", "shared working memory"},
	} {
		_, err := s.Set(ctx, "", "SC-2405", kv[0], kv[1], FormatText, prev)
		require.NoError(t, err)
	}

	_, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-2405", Stage: "fix", TTL: time.Minute, Meta: prev,
	})
	require.NoError(t, err)

	// The lease goes stale and a genuine fix-stage agent takes it over.
	*clock = clock.Add(2 * time.Hour)
	res, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-2405", Stage: "fix", TTL: time.Minute,
		Meta: Meta{Agent: "board-SC-2405-implementation"},
	})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.NotNil(t, res.Displaced)

	assert.NotContains(t, res.InheritedKeys, "stage.pr-review",
		"another step's record must never be handed over as the successor's own work")
	assert.Contains(t, res.InheritedKeys, "fix.evidence", "same-stage state is the whole point of inheritance")
	assert.Contains(t, res.InheritedKeys, "capabilities",
		"un-namespaced working memory is shared, not another step's — dropping it would lose real state")
}

func TestBelongsToOtherStage(t *testing.T) {
	cases := []struct {
		name, key, stage string
		other            bool
	}{
		{"same stage record", "stage.fix", "fix", false},
		{"other stage record", "stage.pr-review", "fix", true},
		{"same stage sub-record", "stage.triage.exit", "triage", false},
		{"other stage sub-record", "stage.triage.exit", "fix", true},
		{"stage-prefixed evidence", "fix.evidence", "fix", false},
		{"un-namespaced shared key", "capabilities", "fix", false},
		{"orchestrator bookkeeping", "budget.fix.attempts", "review", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.other, belongsToOtherStage(c.key, c.stage))
		})
	}
}
