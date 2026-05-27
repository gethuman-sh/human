//go:build linux

package cmddaemon

import (
	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/fusefs"
)

// fuseMount mounts the FUSE .env filter overlay.
// Returns an unmount function (nil if mount failed).
func fuseMount(projectDir string, safe bool, logger zerolog.Logger) func() {
	secMount, err := fusefs.Mount(projectDir, projectDir+"-sec", safe, logger)
	if err != nil {
		logger.Warn().Err(err).Msg("FUSE .env filter not available")
		return nil
	}
	return func() {
		if err := secMount.Unmount(); err != nil {
			logger.Warn().Err(err).Msg("FUSE unmount failed")
		}
	}
}
