//go:build goexperiment.runtimesecret

// DO NOT ENABLE THIS EXPERIMENT WITHOUT READING THIS.
//
// Building with GOEXPERIMENT=runtimesecret segfaults the process on the real
// credential-resolution path (2026-07-31, go1.26.5): `go test .` dies with
// "signal: segmentation fault (core dumped)" and no traceback at all, which
// means the fault is below the runtime's ability to report it. Removing
// secret.Do makes it pass; the experiment alone, and memguard alone, are both
// fine — it is the combination with this path. Two minimal reproducers (exec
// inside Do, values escaping Do under GC) did NOT reproduce it, so the trigger
// is still unidentified.
//
// The package is "not subject to the Go 1 compatibility promise" and this is
// what that means in practice. The file is kept because the erasure is worth
// having once the feature stabilises, and because deleting it would lose the
// finding — but no build enables the tag, so this code is not compiled.

package vault

import "runtime/secret"

// eraseTemporaries runs f in the runtime's secret mode: the registers and stack
// it used are zeroed before this returns, and heap values it allocated are
// zeroed once the collector sees them go unreachable.
//
// It bounds how long the working copies made while reading a secret — the bytes
// coming back from the store, the trimming, the parsing — stay legible in this
// process. Without it they are ordinary garbage, readable for as long as
// nothing happens to overwrite them.
//
// Two limits worth stating rather than discovering. The protection does not
// extend to goroutines f starts, which is why the store's output is read on the
// calling goroutine rather than through the convenience wrapper that spawns a
// copier. And nothing here reaches the store's own process or the kernel pipe
// the bytes travelled through: these are our copies, not every copy.
func eraseTemporaries(f func()) { secret.Do(f) }
