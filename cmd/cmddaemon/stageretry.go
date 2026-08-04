package cmddaemon

import (
	"context"
	"encoding/json"
	"time"

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
//
// project routes the read to the same namespace the agent wrote under — the
// project the ticket key belongs to, resolved by the caller via
// ProjectRegistry.EntryForKey — so a retry never reads another project's
// report for a colliding key.
//
// This caller runs after listStageSettled has already settled the comment
// thread (SC-1484/SC-2133), and has no uniform per-round started-marker to
// anchor an identity check on the way the PR loop's reads do. So rather than
// the full freshness settle, it re-reads with the same bounded backoff only
// while the record is ABSENT — closing the plainer half of the same
// read-after-write race: an exit written just before this read landed, but not
// yet visible to it (SC-2378).
func stageExitClass(ctx context.Context, project, pmKey string, stage daemon.BoardStage, logger zerolog.Logger) (daemon.StageExit, bool) {
	exit, found := readStageExitOnce(ctx, project, pmKey, stage, logger)
	for try := 0; !found && try < prLoopReadRecheckTries-1; try++ {
		select {
		case <-ctx.Done():
			return exit, found
		case <-time.After(prLoopReadRecheckStep):
		}
		exit, found = readStageExitOnce(ctx, project, pmKey, stage, logger)
	}
	return exit, found
}

// readStageExitOnce performs a single, non-retrying read of a stage's exit
// class — extracted so stageExitClass can wrap it in the presence-settle
// backoff above without duplicating the store/parse logic.
func readStageExitOnce(ctx context.Context, project, pmKey string, stage daemon.BoardStage, logger zerolog.Logger) (daemon.StageExit, bool) {
	var exit daemon.StageExit
	var found bool
	err := withStateStore(func(store agentstate.Store) error {
		entry, err := store.Get(ctx, project, pmKey, stageReportName(stage))
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
		// The parse boundary: a bare JSON string becomes the typed vocabulary here.
		exit, found = daemon.StageExit(report.Exit), report.Exit != ""
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
func bumpStageRetries(ctx context.Context, project, pmKey string, stage daemon.BoardStage) (int, error) {
	var n int64
	err := withStateStore(func(store agentstate.Store) error {
		var incrErr error
		n, incrErr = store.Incr(ctx, project, pmKey, retryCounterName(stage), 1,
			agentstate.Meta{Agent: "daemon-board-retry"})
		return incrErr
	})
	return int(n), err
}

// clearStageRetries drops the count after a clean finish, so the next failure
// on this stage gets a full budget rather than the remainder of an older one.
func clearStageRetries(ctx context.Context, project, pmKey string, stage daemon.BoardStage) {
	_ = withStateStore(func(store agentstate.Store) error {
		_, err := store.Delete(ctx, project, pmKey, retryCounterName(stage))
		return err
	})
}

// decStageRetries rolls one automatic-retry charge back — used when a bounded
// relaunch turned out to be a refusal that started nothing, so the budget a real
// crash needs is not spent on a non-event (SC-2989).
func decStageRetries(ctx context.Context, project, pmKey string, stage daemon.BoardStage) {
	_ = withStateStore(func(store agentstate.Store) error {
		_, err := store.Incr(ctx, project, pmKey, retryCounterName(stage), -1,
			agentstate.Meta{Agent: "daemon-board-retry"})
		return err
	})
}
