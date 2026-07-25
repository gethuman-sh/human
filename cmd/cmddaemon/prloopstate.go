package cmddaemon

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
)

// readPRReviewVerdict returns the machine reviewer's verdict recorded in
// stage.pr-review ("" when absent — the loop driver treats that as escalate).
//
// The reviewer's report carries non-string fields (a blocking count), so it is
// read into a typed struct with only the needed scalar rather than a
// map[string]string, which json.Unmarshal rejects on the first non-string value.
func readPRReviewVerdict(ctx context.Context, pmKey string, logger zerolog.Logger) string {
	var v struct {
		Verdict string `json:"verdict"`
	}
	readStageReport(ctx, pmKey, "stage.pr-review", &v, logger)
	return v.Verdict
}

// readPRFixExit returns the fixer's exit recorded in stage.pr-fix ("" when
// absent). Read into a typed struct for the same reason as the verdict.
func readPRFixExit(ctx context.Context, pmKey string, logger zerolog.Logger) string {
	var v struct {
		Exit string `json:"exit"`
	}
	readStageReport(ctx, pmKey, "stage.pr-fix", &v, logger)
	return v.Exit
}

// readStageReport loads one loop step's JSON report from the agent state store
// into out. A missing or unreadable report is not an error the loop can act on
// — it is logged and out is left at its zero value, which the decider treats as
// escalate (never merge on a state it cannot read).
func readStageReport(ctx context.Context, pmKey, name string, out any, logger zerolog.Logger) {
	err := withStateStore(func(store agentstate.Store) error {
		entry, err := store.Get(ctx, pmKey, name)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(entry.Value), out)
	})
	if err != nil {
		logger.Debug().Err(err).Str("pm", pmKey).Str("name", name).Msg("PR loop: no readable stage report")
	}
}
