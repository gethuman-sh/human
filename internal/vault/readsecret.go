package vault

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// readSecretOutput runs a credential helper and returns its stdout, reading it
// on THIS goroutine.
//
// exec.Cmd.Output would be shorter, and it is what this used to call. It hands
// Stdout a bytes.Buffer, which makes os/exec create a pipe and a goroutine to
// copy into it — and the runtime's secret mode explicitly does not extend to
// goroutines started underneath it. The largest copy of the secret, the raw
// bytes coming back from the store, would be written on a goroutine outside the
// erasure while the code that merely trims it was protected. Reading the pipe
// here keeps those bytes on the stack secret mode actually covers.
//
// stderr keeps a copier goroutine, deliberately: it carries op's diagnostic,
// never the secret, and opFailure needs it to turn "exit status 1" into
// something a person can act on.
func readSecretOutput(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary is a static default, args match a whitelisted grammar
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(stdout)
	if err := cmd.Wait(); err != nil {
		// Carry stderr on the ExitError exactly as Output did, so every existing
		// diagnostic path keeps working.
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitErr.Stderr = stderr.Bytes()
		}
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	return out, nil
}
