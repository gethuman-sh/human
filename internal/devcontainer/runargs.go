package devcontainer

import (
	"strings"

	"github.com/rs/zerolog"
)

// ParseRunArgs translates devcontainer.json runArgs (Docker CLI flags) into
// ContainerCreateOptions fields. Unknown flags are logged as warnings.
func ParseRunArgs(args []string, opts *ContainerCreateOptions, logger zerolog.Logger) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, val, hasEq := strings.Cut(arg, "=")

		// Consume the next arg as value for space-separated form.
		if !hasEq && needsValue(key) && i+1 < len(args) {
			i++
			val = args[i]
		}

		applyRunArg(key, val, opts, logger, arg)
	}
}

// needsValue returns true if the flag takes a value argument.
func needsValue(key string) bool {
	switch key {
	case "--add-host", "--cap-add", "--security-opt", "--network":
		return true
	}
	return false
}

// applyRunArg applies a single runArg flag to the create options.
func applyRunArg(key, val string, opts *ContainerCreateOptions, logger zerolog.Logger, raw string) {
	switch key {
	case "--add-host":
		opts.ExtraHosts = appendUnique(opts.ExtraHosts, val)
	case "--cap-add":
		opts.CapAdd = appendUnique(opts.CapAdd, val)
	case "--security-opt":
		opts.SecurityOpt = appendUnique(opts.SecurityOpt, val)
	case "--privileged":
		opts.Privileged = true
	case "--network":
		opts.NetworkMode = val
	default:
		logger.Warn().Str("flag", raw).Msg("unsupported runArg, skipping")
	}
}

// appendUnique adds val unless the list already carries it. These three flags
// are sets, and runArgs are merged on top of entries the manager injects itself
// — so the project's own "--add-host=host.docker.internal:host-gateway"
// duplicates the injected one. Docker writes each entry to /etc/hosts, and the
// duplicate line then makes `getent hosts host.docker.internal` return two
// lines: every consumer that substitutes that output into an address gets a
// value with a newline in it, which is how the container proxy redirect stopped
// being installed at all.
func appendUnique(list []string, val string) []string {
	for _, existing := range list {
		if existing == val {
			return list
		}
	}
	return append(list, val)
}
