package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contextDirID mirrors the docker context store's directory naming
// (digest.FromString(name).Encoded()): the SHA256 hex digest of the context
// name. The store looks up meta.json under this directory, so fixtures must use
// it rather than an arbitrary id.
func contextDirID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// writeConfig writes a config.json with the given currentContext into dir.
func writeConfig(t *testing.T, dir, currentContext string) {
	t.Helper()
	body := `{}`
	if currentContext != "" {
		body = `{"currentContext":"` + currentContext + `"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// writeContext writes a context store meta.json for ctxName with the given
// docker endpoint host, mirroring the docker CLI's on-disk layout
// (contexts/meta/<sha256(name)>/meta.json). The store looks the context up by
// the digest of its name, so the directory must use contextDirID.
func writeContext(t *testing.T, configDir, ctxName, host string) {
	t.Helper()
	metaDir := filepath.Join(configDir, "contexts", "meta", contextDirID(ctxName))
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatalf("mkdir meta: %v", err)
	}
	body := `{"Name":"` + ctxName + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
}

// staticEnv builds a getenv func from a map for deterministic, race-free tests.
func staticEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve(t *testing.T) {
	const unixHost = "unix:///Users/me/.colima/default/docker.sock"
	const npipeHost = "npipe:////./pipe/docker_engine"

	tests := []struct {
		name string
		// setup populates a temp config dir and returns the env map.
		setup       func(t *testing.T, dir string) map[string]string
		wantHost    string
		wantContext string
		wantSource  string
	}{
		{
			name: "DOCKER_HOST set wins, context unset",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "colima")
				writeContext(t, dir, "colima", unixHost)
				return map[string]string{envDockerHost: "tcp://1.2.3.4:2375"}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "env",
		},
		{
			name: "DOCKER_HOST set wins, DOCKER_CONTEXT also set",
			setup: func(t *testing.T, dir string) map[string]string {
				writeContext(t, dir, "colima", unixHost)
				return map[string]string{
					envDockerHost:    "tcp://1.2.3.4:2375",
					envDockerContext: "colima",
				}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "env",
		},
		{
			name: "currentContext resolves unix socket endpoint",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "colima")
				writeContext(t, dir, "colima", unixHost)
				return map[string]string{}
			},
			wantHost:    unixHost,
			wantContext: "colima",
			wantSource:  "context",
		},
		{
			name: "currentContext resolves npipe endpoint (Windows)",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "desktop-windows")
				writeContext(t, dir, "desktop-windows", npipeHost)
				return map[string]string{}
			},
			wantHost:    npipeHost,
			wantContext: "desktop-windows",
			wantSource:  "context",
		},
		{
			name: "DOCKER_CONTEXT overrides config.json currentContext",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "colima")
				writeContext(t, dir, "colima", unixHost)
				writeContext(t, dir, "orbstack", "unix:///Users/me/.orbstack/run/docker.sock")
				return map[string]string{envDockerContext: "orbstack"}
			},
			wantHost:    "unix:///Users/me/.orbstack/run/docker.sock",
			wantContext: "orbstack",
			wantSource:  "context",
		},
		{
			name: "context default returns empty (platform fallback)",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "default")
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "DOCKER_CONTEXT=default returns empty",
			setup: func(t *testing.T, dir string) map[string]string {
				writeContext(t, dir, "colima", unixHost)
				return map[string]string{envDockerContext: defaultContextName}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "no config.json and no store returns empty gracefully",
			setup: func(t *testing.T, _ string) map[string]string {
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "malformed config.json falls back to default",
			setup: func(t *testing.T, dir string) map[string]string {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "currentContext set but meta.json missing falls back to default",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "ghost")
				// No context store entry for "ghost".
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "context present but docker endpoint missing falls back to default",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "weird")
				metaDir := filepath.Join(dir, "contexts", "meta", contextDirID("weird"))
				if err := os.MkdirAll(metaDir, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				// Endpoints map has no "docker" key.
				body := `{"Name":"weird","Metadata":{},"Endpoints":{}}`
				if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(body), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
		{
			name: "context present but host empty falls back to default",
			setup: func(t *testing.T, dir string) map[string]string {
				writeConfig(t, dir, "empty")
				writeContext(t, dir, "empty", "")
				return map[string]string{}
			},
			wantHost:    "",
			wantContext: defaultContextName,
			wantSource:  "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			env := tc.setup(t, dir)
			got := Resolve(withConfigDir(dir), withGetenv(staticEnv(env)))
			if got.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", got.Host, tc.wantHost)
			}
			if got.Context != tc.wantContext {
				t.Errorf("Context = %q, want %q", got.Context, tc.wantContext)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

// TestResolveHonorsDockerConfigDir asserts the resolver reads the injected
// config dir rather than a hardcoded ~/.docker, which is the DOCKER_CONFIG
// requirement in disguise (production wires config.Dir(), which honors
// DOCKER_CONFIG and %USERPROFILE%).
func TestResolveHonorsDockerConfigDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeConfig(t, dirA, "ctxA")
	writeContext(t, dirA, "ctxA", "unix:///a.sock")
	writeConfig(t, dirB, "ctxB")
	writeContext(t, dirB, "ctxB", "unix:///b.sock")

	gotA := Resolve(withConfigDir(dirA), withGetenv(staticEnv(nil)))
	if gotA.Host != "unix:///a.sock" {
		t.Errorf("dirA host = %q, want unix:///a.sock", gotA.Host)
	}
	gotB := Resolve(withConfigDir(dirB), withGetenv(staticEnv(nil)))
	if gotB.Host != "unix:///b.sock" {
		t.Errorf("dirB host = %q, want unix:///b.sock", gotB.Host)
	}
}

// TestResolveEmptyConfigDir guards the configDir=="" branch.
func TestResolveEmptyConfigDir(t *testing.T) {
	got := Resolve(withConfigDir(""), withGetenv(staticEnv(map[string]string{envDockerContext: "colima"})))
	if got.Host != "" || got.Source != "default" {
		t.Errorf("empty configDir: got %+v, want default fallback", got)
	}
}

func TestUnreachableError(t *testing.T) {
	cause := errors.New("dial unix /no/socket: connect: no such file")

	t.Run("named context with resolved host", func(t *testing.T) {
		err := UnreachableError(cause, Result{Host: "unix:///x.sock", Context: "colima", Source: "context"})
		msg := err.Error()
		for _, want := range []string{"colima", "unix:///x.sock", "docker context use"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q missing %q", msg, want)
			}
		}
		if strings.Contains(msg, "starting agent container") {
			t.Errorf("error must not be the bare opaque message: %q", msg)
		}
		if !errors.Is(err, cause) {
			t.Errorf("cause must be preserved in the chain")
		}
	})

	t.Run("default context uses platform default endpoint", func(t *testing.T) {
		err := UnreachableError(cause, Result{Host: "", Context: defaultContextName, Source: "default"})
		msg := err.Error()
		if !strings.Contains(msg, defaultContextName) {
			t.Errorf("error %q missing context name", msg)
		}
		if !strings.Contains(msg, platformDefaultEndpoint()) {
			t.Errorf("error %q missing platform default endpoint %q", msg, platformDefaultEndpoint())
		}
	})
}

func TestPlatformDefaultEndpoint(t *testing.T) {
	// The endpoint must be a concrete, non-empty scheme so error messages are
	// actionable on every platform.
	ep := platformDefaultEndpoint()
	if ep == "" {
		t.Fatal("platform default endpoint must not be empty")
	}
	if !strings.Contains(ep, "://") {
		t.Errorf("platform default endpoint %q lacks a scheme", ep)
	}
}

func TestOsGetenv(t *testing.T) {
	t.Setenv("DOCKERHOST_TEST_VAR", "value")
	if osGetenv("DOCKERHOST_TEST_VAR") != "value" {
		t.Errorf("osGetenv did not read the environment")
	}
}
