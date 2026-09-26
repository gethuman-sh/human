// Package gotoolchain answers one question in one place: does the Go toolchain
// present here satisfy what the project's go.mod requires? An image whose Go is
// older than go.mod fails EVERY go invocation at toolchain selection — and where
// the module proxy is unreachable it cannot recover, so the failure arrives as an
// unrelated-looking verification error deep inside a run (SC-5879).
package gotoolchain

import (
	"context"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// ErrUndecidable means the comparison could not be made — an unparseable or
// missing version on either side. Reported as itself rather than as a mismatch:
// a check that refuses on evidence it does not have starts lying.
var ErrUndecidable = errors.WithDetails("go version comparison is undecidable")

// ErrNoDirective means go.mod exists but this function found no `go`
// directive it could read — different from no go.mod at all: the file is
// there, something about reading its directive is wrong (a stray trailing
// comment survives this parse; only a wholly malformed line reaches here).
// Distinguishing the two matters because the caller's message otherwise says
// "no go.mod in <dir>" about a directory that has one (SC-5879 review).
var ErrNoDirective = errors.WithDetails("go.mod names no readable `go` directive")

// GoDirective returns the version named by go.mod's `go` directive ("1.26.6"),
// or "" when there is none. Only the directive line is read: `toolchain go1.x`
// and the require block name versions that are not the project's own floor. A
// trailing `//` comment is stripped before splitting fields, so
// `go 1.26.6 // pinned` still yields "1.26.6" rather than silently disabling
// the pin — a go.mod is free to comment its directive and this must not treat
// that as absence.
func GoDirective(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			return fields[1]
		}
	}
	return ""
}

// Requirement reads the version dir/go.mod requires. ok is false when dir holds
// no go.mod — not every project is a Go project, and that is not a fault. A
// go.mod that exists but names no readable directive is reported as
// ErrNoDirective rather than folded into the same "no go.mod" case, which
// would misname the fault (SC-5879 review).
func Requirement(dir string) (version string, ok bool, err error) {
	path := filepath.Join(dir, "go.mod")
	data, readErr := os.ReadFile(path) // #nosec G304 -- path is <dir>/go.mod
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", false, nil
		}
		return "", false, errors.WrapWithDetails(readErr, "reading go.mod", "path", path)
	}
	v := GoDirective(data)
	if v == "" {
		return "", false, ErrNoDirective
	}
	return v, true, nil
}

// Prober answers what Go is installed here. An interface so the check is
// testable without a second Go installation on the machine running the tests.
type Prober interface {
	Installed(ctx context.Context) (string, error)
}

// OSProber asks the go on PATH, with GOTOOLCHAIN=local so the question cannot
// be answered by the very toolchain switch under suspicion — unset, `go env`
// itself fails when go.mod names a toolchain it cannot fetch.
type OSProber struct{}

// Installed returns the running toolchain's version without the "go" prefix
// ("1.26.5").
func (OSProber) Installed(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION") // #nosec G204 -- fixed argv
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		return "", errors.WrapWithDetails(err, "asking go for its version")
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "go"), nil
}

// Check reports whether installed satisfies required. Both are bare versions
// ("1.26.6"); go/version compares them the way the toolchain itself does, so a
// go.mod floor of "1.26" is satisfied by 1.26.5.
func Check(required, installed string) error {
	if required == "" || !version.IsValid("go"+required) || !version.IsValid("go"+installed) {
		return ErrUndecidable
	}
	if version.Compare("go"+required, "go"+installed) > 0 {
		return errors.WithDetails(
			"go.mod requires go "+required+" but the toolchain here is go "+installed,
			"required", required, "installed", installed)
	}
	return nil
}
