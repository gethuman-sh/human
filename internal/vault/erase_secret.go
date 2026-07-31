//go:build goexperiment.runtimesecret

// Enabling this used to segfault the process, and the reason is worth keeping:
// the erasure fought the WebAssembly runtime (extism/wazero) inside the
// 1Password SDK, which ran JIT-compiled code on stacks the erasure then walked.
// No traceback — the fault was below the runtime's ability to report it. Every
// pure-Go reproducer survived; flipping CGO alone flipped the result.
//
// The SDK is gone (SC-2183) and with it the only wasm in this binary, so the
// erasure is enabled again in every build. Before adding a dependency that
// embeds a wasm runtime, check `go version -m` and re-run the suite with
// GOEXPERIMENT=runtimesecret — this package is not covered by the Go 1
// compatibility promise, and that is what it means in practice.

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
