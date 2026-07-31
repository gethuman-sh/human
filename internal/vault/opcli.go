package vault

import (
	"context"
	stderrors "errors"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/platform"
)

// opRefPattern whitelists the characters permitted in a resolved
// op:// reference. The set intentionally excludes shell metacharacters
// and flag introducers so no value that slips past the prefix check
// can reach the CLI as a rogue argument.
var opRefPattern = regexp.MustCompile(`^op://[A-Za-z0-9 _./\-]+$`)

// opTimeout bounds one op invocation. It must be generous enough for a cold
// CLI start and an unlock round-trip, yet short enough that an unresponsive
// CLI does not stall every caller waiting on a secret.
const opTimeout = 30 * time.Second

// OpCLI resolves 1pw:// secret references by shelling out to the 1Password CLI.
// It is the fallback behind the in-process SDK on every platform: released
// binaries are built without CGO, so the SDK is unavailable and the CLI is
// the working path. On WSL2 the Windows op.exe is used across the boundary.
type OpCLI struct {
	// Binary is the op CLI binary name. Defaults to "op" ("op.exe" under WSL).
	Binary string

	// runner overrides command execution for testing.
	runner func(ctx context.Context, binary string, args ...string) ([]byte, error)
}

// NewOpCLI creates a 1Password CLI provider with the op binary for the
// current platform.
func NewOpCLI() *OpCLI {
	return &OpCLI{Binary: opBinary()}
}

// opBinary returns the op CLI binary name for the current platform: the
// Windows op.exe under WSL, plain op everywhere else.
func opBinary() string {
	if platform.IsWSL() {
		return "op.exe"
	}
	return "op"
}

// CanResolve reports whether ref is a 1Password reference (1pw:// prefix).
func (o *OpCLI) CanResolve(ref string) bool {
	return strings.HasPrefix(ref, secretRefPrefix)
}

// Resolve shells out to the op CLI to retrieve the secret value for the given
// reference. It translates the 1pw:// prefix to op:// before calling the CLI.
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

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	var out []byte
	var err error
	if o.runner != nil {
		out, err = o.runner(ctx, o.Binary, "read", sdkRef)
	} else {
		out, err = readSecretOutput(ctx, o.Binary, "read", sdkRef)
	}
	if err != nil {
		return "", opFailure(o.Binary, ref, err, ctx.Err())
	}
	return strings.TrimSpace(string(out)), nil
}

// opFailure builds the error for a failed op invocation.
//
// The MESSAGE has to carry the diagnosis, not just the attached details: only
// err.Error() survives the daemon→client hops, so a board banner shows the
// message alone. The old one — "resolving 1Password secret via CLI" — named no
// secret, no cause and no remedy, so a transient blip and a permanently signed-out
// CLI were indistinguishable, and with several references configured it did not
// even say WHICH one failed (SC-2005).
//
// The reference is safe to show: it is a vault/item/field path that already sits
// in .humanconfig, never the secret it points at. op's own stderr is included
// because it is the line that actually tells you what to do ("not signed in",
// "item not found"), and the distinct failure shapes — binary absent, timed out,
// non-zero exit — are separated because their remedies are different.
func opFailure(binary, ref string, err error, ctxErr error) error {
	details := []any{"ref", ref, "binary", binary}
	switch {
	case ctxErr != nil:
		return tagCause(ErrStoreUnreachable,
			"1Password CLI "+binary+" timed out after "+opTimeout.String()+" reading "+ref+
				" — the CLI is unresponsive or waiting on an unlock prompt",
			append(details, "timeout", opTimeout.String())...)

	case stderrors.Is(err, exec.ErrNotFound):
		return tagCause(ErrStoreUnreachable,
			"1Password CLI "+binary+" not found on PATH, needed to read "+ref+
				" — install it, or use env vars instead of a 1pw:// reference",
			details...)
	}

	// .Output() stashes the command's stderr on *exec.ExitError; surfacing it
	// turns an opaque "exit status 1" into op's actual diagnostic.
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		diag := strings.TrimSpace(string(exitErr.Stderr))
		if diag == "" {
			diag = "no diagnostic on stderr"
		}
		return tagCause(classifySecretStderr(diag),
			"1Password CLI "+binary+" could not read "+ref+": "+diag,
			append(details, "stderr", diag, "exit_code", exitErr.ExitCode())...)
	}
	return tagCause(ErrCauseUndetermined,
		"1Password CLI "+binary+" could not read "+ref+": "+err.Error(), details...)
}
