package vault

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// opRefPattern whitelists the characters permitted in a resolved
// op:// reference. The set intentionally excludes shell metacharacters
// and flag introducers so no value that slips past the prefix check
// can reach the CLI as a rogue argument.
var opRefPattern = regexp.MustCompile(`^op://[A-Za-z0-9 _./\-]+$`)

// OpCLI resolves 1pw:// secret references by shelling out to the 1Password CLI.
// This is the fallback for WSL2 where the Go SDK cannot reach the Windows
// 1Password desktop app.
type OpCLI struct {
	// Binary is the op CLI binary name. Defaults to "op.exe" for WSL2.
	Binary string

	// runner overrides command execution for testing.
	runner func(ctx context.Context, binary string, args ...string) ([]byte, error)
}

// NewOpCLI creates a 1Password CLI provider for WSL2.
func NewOpCLI() *OpCLI {
	return &OpCLI{Binary: "op.exe"}
}

// CanResolve reports whether ref is a 1Password reference (1pw:// prefix).
func (o *OpCLI) CanResolve(ref string) bool {
	return strings.HasPrefix(ref, secretRefPrefix)
}

// Resolve shells out to op.exe to retrieve the secret value for the given reference.
// It translates the 1pw:// prefix to op:// before calling the CLI.
func (o *OpCLI) Resolve(ref string) (string, error) {
	sdkRef := sdkRefPrefix + strings.TrimPrefix(ref, secretRefPrefix)

	// Validate that the resolved reference matches the whitelisted
	// grammar. The prefix check alone is not enough: a value like
	// "op://--version" passes HasPrefix but could be interpreted as a
	// CLI flag by op.
	if !opRefPattern.MatchString(sdkRef) {
		return "", errors.WithDetails("invalid secret reference: must match op://<vault>/<item>/<field>",
			"ref", ref)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out []byte
	var err error
	if o.runner != nil {
		out, err = o.runner(ctx, o.Binary, "read", sdkRef)
	} else {
		out, err = exec.CommandContext(ctx, o.Binary, "read", sdkRef).Output() // #nosec G204 -- binary is a static default, ref is from config
	}
	if err != nil {
		// .Output() stashes the command's stderr on *exec.ExitError; surfacing
		// it turns an opaque "exit status 1" into the actual op diagnostic.
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", errors.WrapWithDetails(err, "resolving 1Password secret via CLI",
				"ref", ref, "stderr", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", errors.WrapWithDetails(err, "resolving 1Password secret via CLI", "ref", ref)
	}
	return strings.TrimSpace(string(out)), nil
}
