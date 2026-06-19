//go:build !windows

package dockerhost

import "os"

// osGetenv is the production environment lookup.
func osGetenv(key string) string { return os.Getenv(key) }

// platformDefaultEndpoint is the SDK's compiled-in default on unix-like
// systems (macOS, Linux, including rootless/Podman before context resolution).
// Used only to render an actionable error; the SDK itself supplies this default
// when Host is empty.
func platformDefaultEndpoint() string { return "unix:///var/run/docker.sock" }
