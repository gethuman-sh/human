//go:build !goexperiment.runtimesecret

package vault

// eraseTemporaries runs f unchanged. runtime/secret exists only under
// GOEXPERIMENT=runtimesecret, so a build without it keeps the previous
// behaviour: the working copies made while reading a secret are ordinary
// garbage, collected whenever the collector gets to them.
func eraseTemporaries(f func()) { f() }
