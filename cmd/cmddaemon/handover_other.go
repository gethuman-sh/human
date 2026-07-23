//go:build windows

package cmddaemon

import (
	"context"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/daemon"
)

// maybeWatchBinary is a no-op on Windows: gapless socket handover relies on
// passing listener fds to a re-exec'd child (os/exec ExtraFiles), which Windows
// does not support. Rebuilds are picked up with a manual `human daemon stop`
// then `human daemon start`.
func maybeWatchBinary(_ context.Context, _ *listenerSet, _ *daemon.Server, _ func() int64, _ context.CancelFunc, _ *atomic.Bool, logger zerolog.Logger) {
	logger.Info().Msg("daemon self-restart on binary change is not supported on Windows; rebuild then `human daemon stop && human daemon start`")
}
