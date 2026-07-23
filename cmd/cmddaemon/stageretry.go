package cmddaemon

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// retryCounterName is where a stage's automatic-retry count lives, alongside
// the run's other working state. Keeping it in the same store the agents use
// means the count survives a daemon restart and is visible with `human state
// list <KEY>` when someone asks why a card stopped retrying.
func retryCounterName(stage daemon.BoardStage) string {
	return "relaunch." + string(stage) + ".attempts"
}

func stageReportName(stage daemon.BoardStage) string {
	return "stage." + string(stage)
}

// withStateStore opens the agent state store for one operation. Opening per
// call (rather than holding a handle) keeps the daemon's failure path free of
// a long-lived lock on a database the agents write concurrently.
func withStateStore(fn func(agentstate.Store) error) error {
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return fn(store)
}

// stageExitClass reads the exit class a stage recorded before returning.
//
// The second result reports whether an outcome was found at all: a stage whose
// agent died before writing one leaves nothing, and the retry policy treats
// that absence as retryable rather than terminal.
func stageExitClass(ctx context.Context, pmKey string, stage daemon.BoardStage, logger zerolog.Logger) (string, bool) {
	var exit string
	var found bool
	err := withStateStore(func(store agentstate.Store) error {
		entry, err := store.Get(ctx, pmKey, stageReportName(stage))
		if err != nil {
			return err
		}
		var report struct {
			Exit string `json:"exit"`
		}
		if jsonErr := json.Unmarshal([]byte(entry.Value), &report); jsonErr != nil {
			// A stage report we cannot parse is not an outcome we can act on.
			return jsonErr
		}
		exit, found = report.Exit, report.Exit != ""
		return nil
	})
	if err != nil {
		logger.Debug().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board retry: no readable stage report")
		return "", false
	}
	return exit, found
}

// bumpStageRetries increments and returns this stage's automatic-retry count.
func bumpStageRetries(ctx context.Context, pmKey string, stage daemon.BoardStage) (int, error) {
	var n int64
	err := withStateStore(func(store agentstate.Store) error {
		var incrErr error
		n, incrErr = store.Incr(ctx, pmKey, retryCounterName(stage), 1,
			agentstate.Meta{Agent: "daemon-board-retry"})
		return incrErr
	})
	return int(n), err
}

// clearStageRetries drops the count after a clean finish, so the next failure
// on this stage gets a full budget rather than the remainder of an older one.
func clearStageRetries(ctx context.Context, pmKey string, stage daemon.BoardStage) {
	_ = withStateStore(func(store agentstate.Store) error {
		_, err := store.Delete(ctx, pmKey, retryCounterName(stage))
		return err
	})
}
