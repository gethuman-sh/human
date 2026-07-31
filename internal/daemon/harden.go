package daemon

import (
	"os"

	"github.com/rs/zerolog"
)

// AllowDebugEnv opts out of process hardening for an operator who genuinely
// needs to attach a debugger. Hardening blocks ptrace, so without an escape
// hatch the only way to debug a daemon would be to patch and rebuild it.
const AllowDebugEnv = "HUMAN_DAEMON_ALLOW_DEBUG"

// HardenProcess applies the platform's process-wide protections and reports
// what it managed, so the log records the protection actually in force rather
// than the protection intended.
//
// A failure never stops the daemon starting. These measures protect credentials
// against accidental disclosure; refusing to run without them would trade a
// confidentiality improvement for an availability outage, which on this
// machine is the more expensive failure by a wide margin.
func HardenProcess(logger zerolog.Logger) {
	if os.Getenv(AllowDebugEnv) != "" {
		logger.Warn().Str("env", AllowDebugEnv).
			Msg("process hardening skipped: this daemon can be attached to and core-dumped")
		return
	}
	applied, err := hardenProcess()
	if err != nil {
		logger.Warn().Err(err).Strs("applied", applied).
			Msg("process hardening incomplete: the daemon's credentials may reach a core dump")
		return
	}
	if len(applied) == 0 {
		logger.Info().Msg("process hardening unavailable on this platform")
		return
	}
	logger.Info().Strs("applied", applied).Msg("process hardened")
}
