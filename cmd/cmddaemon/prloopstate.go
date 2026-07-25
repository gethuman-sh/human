package cmddaemon

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
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

// readPRFixReport loads the fixer's stage.pr-fix report: its exit, the optional
// enumerated directions it recorded on needs-input, and a one-line context
// (deferred comments, else the summary) for the options block. Absent fields
// stay zero — the loop driver treats a missing exit as escalate.
func readPRFixReport(ctx context.Context, pmKey string, logger zerolog.Logger) (exit string, options []daemon.BoardOption, summary string) {
	var v struct {
		Exit     string               `json:"exit"`
		Options  []daemon.BoardOption `json:"options"`
		Deferred string               `json:"deferred"`
		Summary  string               `json:"summary"`
	}
	readStageReport(ctx, pmKey, "stage.pr-fix", &v, logger)
	summary = v.Deferred
	if summary == "" {
		summary = v.Summary
	}
	return v.Exit, v.Options, summary
}

// readDeployFixExit returns the deploy fixer's exit recorded in stage.deploy-fix
// ("" when absent — the driver treats a non-done exit, including absence, as red).
func readDeployFixExit(ctx context.Context, pmKey string, logger zerolog.Logger) string {
	var v struct {
		Exit string `json:"exit"`
	}
	readStageReport(ctx, pmKey, "stage.deploy-fix", &v, logger)
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
