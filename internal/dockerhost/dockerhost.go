// Package dockerhost resolves the Docker Engine endpoint the same way the
// docker CLI does, so human reaches the engine out-of-the-box on setups that
// expose the socket via a docker *context* (colima, OrbStack, Rancher Desktop,
// Docker Desktop, Podman) rather than via DOCKER_HOST.
//
// The Docker Go SDK's client.FromEnv only reads environment variables; it does
// not consult the docker CLI context store. This package bridges that gap with
// a single shared resolver used by every Docker client constructor in human so
// the two can never diverge.
package dockerhost

import (
	"path/filepath"

	"github.com/docker/cli/cli/config"
	ctxstore "github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/context/store"

	"github.com/gethuman-sh/human/errors"
)

// Environment variable names. We use literals rather than importing the moby
// client constants to keep this package's dependency surface minimal.
const (
	envDockerHost    = "DOCKER_HOST"
	envDockerContext = "DOCKER_CONTEXT"
)

// defaultContextName is the reserved name for the env/config-default context,
// for which the SDK's compiled-in platform default socket/pipe must be used.
const defaultContextName = "default"

// contextsDir is the subdirectory of the docker config dir that holds the
// context store, mirroring the docker CLI's layout.
const contextsDir = "contexts"

// Result describes the outcome of context resolution. Host is the endpoint to
// hand to the Docker SDK (empty means "use the SDK's env/platform default").
// Context and Source explain *why* that host was chosen so callers can build an
// actionable error when the engine turns out to be unreachable.
type Result struct {
	// Host is the resolved Docker endpoint, or "" when the caller should fall
	// back to the SDK's env/platform default.
	Host string
	// Context is the active docker context name ("default" when none).
	Context string
	// Source explains where Host came from: "env" (DOCKER_HOST set), "context"
	// (resolved from the context store), or "default" (platform fallback).
	Source string
}

// options carries the injectable dependencies so the resolver stays a pure
// function of (env, filesystem) and is exhaustively unit-testable without
// mutating process-global state.
type options struct {
	// getenv looks up an environment variable; injected for tests.
	getenv func(string) string
	// configDir is the docker config dir (the ".docker" directory). It honors
	// DOCKER_CONFIG and per-platform defaults in production; tests inject a temp
	// dir. config.json lives directly under it; the context store under
	// configDir/contexts.
	configDir string
}

// Option customizes resolution. Production callers pass none and get the real
// os/config-backed behavior.
type Option func(*options)

// withGetenv injects an environment lookup. Used by tests.
func withGetenv(fn func(string) string) Option {
	return func(o *options) { o.getenv = fn }
}

// withConfigDir injects the docker config dir. Used by tests.
func withConfigDir(dir string) Option {
	return func(o *options) { o.configDir = dir }
}

// Resolve mirrors the docker CLI's context precedence:
//
//  1. DOCKER_HOST set  => explicit env wins; return Host="" so the SDK's FromEnv
//     handles it (Source "env").
//  2. otherwise context name = DOCKER_CONTEXT, else config.json currentContext,
//     else "default".
//  3. context == "default" => Host="" so the SDK uses the platform default
//     socket/pipe (Source "default").
//  4. otherwise read Endpoints.docker.Host from the context store and return it
//     (Source "context").
//
// Any failure to read the store (missing/malformed config.json, missing
// meta.json, missing docker endpoint) degrades gracefully to the platform
// default rather than blocking startup.
func Resolve(opts ...Option) Result {
	o := options{
		getenv:    osGetenv,
		configDir: config.Dir(),
	}
	for _, fn := range opts {
		fn(&o)
	}

	// Explicit DOCKER_HOST always wins for predictable override.
	if o.getenv(envDockerHost) != "" {
		return Result{Host: "", Context: defaultContextName, Source: "env"}
	}

	ctxName := o.getenv(envDockerContext)
	if ctxName == "" {
		ctxName = currentContextFromConfig(&o)
	}
	if ctxName == "" || ctxName == defaultContextName {
		return Result{Host: "", Context: defaultContextName, Source: "default"}
	}

	host, ok := endpointHost(&o, ctxName)
	if !ok {
		// Store missing/malformed for this context: fall back to the platform
		// default rather than failing hard.
		return Result{Host: "", Context: defaultContextName, Source: "default"}
	}
	return Result{Host: host, Context: ctxName, Source: "context"}
}

// currentContextFromConfig reads currentContext from the docker config.json.
// A missing or malformed config yields "" (caller treats as default).
func currentContextFromConfig(o *options) string {
	if o.configDir == "" {
		return ""
	}
	cfg, err := config.Load(o.configDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.CurrentContext
}

// endpointHost reads Endpoints.docker.Host for ctxName from the context store.
// The bool is false on any error so the caller can fall back to the default.
func endpointHost(o *options, ctxName string) (string, bool) {
	if o.configDir == "" {
		return "", false
	}
	storeDir := filepath.Join(o.configDir, contextsDir)
	// Register the docker endpoint type so the store can unmarshal the typed
	// EndpointMeta (Host/SkipTLSVerify) from meta.json.
	cfg := store.NewConfig(
		func() any { return &map[string]any{} },
		store.EndpointTypeGetter(ctxstore.DockerEndpoint, func() any { return &ctxstore.EndpointMeta{} }),
	)
	s := store.New(storeDir, cfg)
	meta, err := s.GetMetadata(ctxName)
	if err != nil {
		return "", false
	}
	ep, err := ctxstore.EndpointFromContext(meta)
	if err != nil {
		return "", false
	}
	if ep.Host == "" {
		return "", false
	}
	return ep.Host, true
}

// UnreachableError builds an actionable error for a failed Docker connection.
// It names the active context and the attempted endpoint and gives a one-line
// remedy, replacing the opaque generic failure users used to see.
func UnreachableError(cause error, r Result) error {
	endpoint := r.Host
	if endpoint == "" {
		endpoint = platformDefaultEndpoint()
	}
	remedy := "start your Docker engine, or run `docker context use <name>` to select a reachable context"
	return errors.WrapWithDetails(
		cause,
		"cannot reach the Docker engine (context %q, endpoint %q): %s",
		"context", r.Context,
		"endpoint", endpoint,
		"remedy", remedy,
	)
}
