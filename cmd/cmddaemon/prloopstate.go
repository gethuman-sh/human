package cmddaemon

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// readPRReviewVerdict returns the machine reviewer's verdict recorded in
// stage.pr-review, and whether a report was found at all. Absence is reported
// separately from an empty verdict because they are different failures: a step
// that recorded nothing died or never got that far, while a step that recorded
// something unrecognized decided something the daemon cannot read. Both
// escalate; only the message differs (SC-1892).
//
// The reviewer's report carries non-string fields (a blocking count), so it is
// read into a typed struct with only the needed scalars rather than a
// map[string]string, which json.Unmarshal rejects on the first non-string value.
//
// head is the branch-tip SHA the reviewer actually read (the local ref, not
// origin's — SC-1760). The loop's convergence guard compares it against the head
// the following fix leaves behind, so a fix that adds no commit escalates instead
// of driving an endless re-review.
func readPRReviewVerdict(ctx context.Context, pmKey string, logger zerolog.Logger) (verdict, head string, recorded bool) {
	var v struct {
		Verdict string `json:"verdict"`
		Head    string `json:"head"`
	}
	recorded = readStageReport(ctx, pmKey, "stage.pr-review", &v, logger)
	return v.Verdict, v.Head, recorded
}

// readPRFixReport loads the fixer's stage.pr-fix report: its exit, the optional
// enumerated directions it recorded on needs-input, a one-line context (deferred
// comments, else the summary) for the options block, the branch-tip SHA it left
// behind (head — fed to the loop's convergence guard), plus whether a report was
// found at all. Absent fields stay zero — the loop driver treats a missing exit
// as escalate.
func readPRFixReport(ctx context.Context, pmKey string, logger zerolog.Logger) (exit string, options []daemon.BoardOption, summary, head string, recorded bool) {
	var v struct {
		Exit     string               `json:"exit"`
		Options  []daemon.BoardOption `json:"options"`
		Deferred string               `json:"deferred"`
		Summary  string               `json:"summary"`
		Head     string               `json:"head"`
	}
	recorded = readStageReport(ctx, pmKey, "stage.pr-fix", &v, logger)
	summary = v.Deferred
	if summary == "" {
		summary = v.Summary
	}
	return v.Exit, v.Options, summary, v.Head, recorded
}

// readDeployFixExit returns the deploy fixer's exit recorded in stage.deploy-fix
// ("" when absent — the driver treats a non-done exit, including absence, as red).
func readDeployFixExit(ctx context.Context, pmKey string, logger zerolog.Logger) string {
	var v struct {
		Exit string `json:"exit"`
	}
	_ = readStageReport(ctx, pmKey, "stage.deploy-fix", &v, logger)
	return v.Exit
}

// readStageReport loads one loop step's JSON report from the agent state store
// into out, and reports whether it found one. A missing or unreadable report is
// not an error the loop can act on — it is logged and out is left at its zero
// value, which the decider treats as escalate (never merge on a state it cannot
// read) — but the caller still needs to know it was MISSING rather than empty,
// so the escalation can say which.
func readStageReport(ctx context.Context, pmKey, name string, out any, logger zerolog.Logger) bool {
	err := withStateStore(func(store agentstate.Store) error {
		entry, err := store.Get(ctx, pmKey, name)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(entry.Value), out)
	})
	if err != nil {
		logger.Debug().Err(err).Str("pm", pmKey).Str("name", name).Msg("PR loop: no readable stage report")
		return false
	}
	return true
}
