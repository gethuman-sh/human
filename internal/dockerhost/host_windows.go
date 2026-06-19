//go:build windows

package dockerhost

import "os"

// osGetenv is the production environment lookup.
func osGetenv(key string) string { return os.Getenv(key) }

// platformDefaultEndpoint is the SDK's compiled-in default on Windows: the
// Docker Desktop named pipe. Used only to render an actionable error; the SDK
// itself supplies this default when Host is empty.
func platformDefaultEndpoint() string { return "npipe:////./pipe/docker_engine" }
