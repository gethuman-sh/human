package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// isInstallRun reports whether an exec call is a feature's install.sh run (as
// opposed to the staging mkdir, cleanup, or the base-env printenv probe).
func isInstallRun(c mockExecCall) bool {
	return len(c.Cmd) > 0 && strings.Contains(c.Cmd[len(c.Cmd)-1], "./install.sh")
}

func findInstallRun(t *testing.T, calls []mockExecCall) mockExecCall {
	t.Helper()
	for _, c := range calls {
		if isInstallRun(c) {
			return c
		}
	}
	t.Fatal("no install.sh run exec call found")
	return mockExecCall{}
}

func TestFeatureEnv_BasicOptions(t *testing.T) {
	opts := map[string]any{
		"version": "22",
	}
	meta := &FeatureMeta{
		Options: map[string]FeatureOption{
			"version": {Type: "string", Default: "lts"},
		},
	}
	env := featureEnv(opts, meta, "vscode")

	envMap := toEnvMap(env)
	if envMap["VERSION"] != "22" {
		t.Errorf("VERSION = %q, want %q", envMap["VERSION"], "22")
	}
	if envMap["_REMOTE_USER"] != "vscode" {
		t.Errorf("_REMOTE_USER = %q", envMap["_REMOTE_USER"])
	}
	if envMap["_REMOTE_USER_HOME"] != "/home/vscode" {
		t.Errorf("_REMOTE_USER_HOME = %q", envMap["_REMOTE_USER_HOME"])
	}
}

func TestFeatureEnv_Defaults(t *testing.T) {
	meta := &FeatureMeta{
		Options: map[string]FeatureOption{
			"version": {Type: "string", Default: "lts"},
			"install": {Type: "boolean", Default: true},
		},
	}
	env := featureEnv(nil, meta, "root")

	envMap := toEnvMap(env)
	if envMap["VERSION"] != "lts" {
		t.Errorf("VERSION = %q, want %q", envMap["VERSION"], "lts")
	}
	if envMap["INSTALL"] != "true" {
		t.Errorf("INSTALL = %q, want %q", envMap["INSTALL"], "true")
	}
	if envMap["_REMOTE_USER_HOME"] != "/root" {
		t.Errorf("_REMOTE_USER_HOME = %q", envMap["_REMOTE_USER_HOME"])
	}
}

func TestFeatureEnv_OverridesDefaults(t *testing.T) {
	opts := map[string]any{"version": "20"}
	meta := &FeatureMeta{
		Options: map[string]FeatureOption{
			"version": {Type: "string", Default: "lts"},
		},
	}
	env := featureEnv(opts, meta, "vscode")
	envMap := toEnvMap(env)
	if envMap["VERSION"] != "20" {
		t.Errorf("VERSION = %q, want %q (user override should win)", envMap["VERSION"], "20")
	}
}

func TestExtractFeatureMeta(t *testing.T) {
	tarData := buildFeatureTar(t, "test", "1.0.0")
	parsedMeta, err := extractFeatureMeta(tarData, "test-ref")
	if err != nil {
		t.Fatal(err)
	}
	if parsedMeta.ID != "test" {
		t.Errorf("meta.ID = %q", parsedMeta.ID)
	}
}

func TestExtractFeatureMeta_NoMetaFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "./install.sh", Size: 5, Mode: 0o755})
	_, _ = tw.Write([]byte("#!/sh"))
	_ = tw.Close()

	meta, err := extractFeatureMeta(buf.Bytes(), "test-ref")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "" {
		t.Errorf("expected empty meta ID, got %q", meta.ID)
	}
}

// buildFeatureTar creates a minimal feature tarball for testing.
func buildFeatureTar(t *testing.T, id, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	script := []byte("#!/bin/sh\necho hello\n")
	_ = tw.WriteHeader(&tar.Header{Name: "./install.sh", Size: int64(len(script)), Mode: 0o755})
	_, _ = tw.Write(script)

	meta := FeatureMeta{ID: id, Version: version, Name: "Test Feature"}
	metaJSON, _ := json.Marshal(meta)
	_ = tw.WriteHeader(&tar.Header{Name: "./devcontainer-feature.json", Size: int64(len(metaJSON)), Mode: 0o644})
	_, _ = tw.Write(metaJSON)
	_ = tw.Close()
	return buf.Bytes()
}

func TestInstallFeatures_ExecCalls(t *testing.T) {
	mock := &mockDockerClient{}

	meta := &FeatureMeta{
		ID:      "node",
		Options: map[string]FeatureOption{"version": {Default: "lts"}},
	}
	tarData := buildFeatureTar(t, "node", "1.0.0")
	puller := &mockFeaturePuller{
		tarData: tarData,
		meta:    meta,
	}

	features := map[string]any{
		"ghcr.io/devcontainers/features/node:1": map[string]any{"version": "22"},
	}

	_, err := InstallFeatures(context.Background(), mock, puller, "container-123",
		features, "vscode", testLogger(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}

	// The install.sh run call carries the feature env vars.
	runCall := findInstallRun(t, mock.execCalls)
	if runCall.Opts.User != "root" {
		t.Errorf("run call user = %q, want root", runCall.Opts.User)
	}
	envMap := toEnvMap(runCall.Opts.Env)
	if envMap["VERSION"] != "22" {
		t.Errorf("VERSION env = %q, want 22", envMap["VERSION"])
	}
	if envMap["_REMOTE_USER"] != "vscode" {
		t.Errorf("_REMOTE_USER = %q", envMap["_REMOTE_USER"])
	}
}

func TestInstallFeatures_InstallsInDependencyOrder(t *testing.T) {
	mock := &mockDockerClient{}

	claude := "ghcr.io/anthropics/devcontainer-features/claude-code:1"
	node := "ghcr.io/devcontainers/features/node:1"

	// Each feature carries a distinct option default so its run exec call can be
	// attributed back to the feature via its env var.
	puller := &mockFeaturePuller{
		tarData: buildFeatureTar(t, "shared", "1.0.0"),
		metaByRef: map[string]*FeatureMeta{
			claude: {ID: "claude-code", InstallsAfter: []string{"ghcr.io/devcontainers/features/node"},
				Options: map[string]FeatureOption{"marker": {Default: "claude"}}},
			node: {ID: "node",
				Options: map[string]FeatureOption{"marker": {Default: "node"}}},
		},
	}

	features := map[string]any{claude: map[string]any{}, node: map[string]any{}}

	if _, err := InstallFeatures(context.Background(), mock, puller, "container-123",
		features, "vscode", testLogger(), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}

	// Each feature's install.sh run carries the MARKER env identifying it; the
	// order of those runs is the install order.
	var installOrder []string
	for _, call := range mock.execCalls {
		if isInstallRun(call) {
			installOrder = append(installOrder, toEnvMap(call.Opts.Env)["MARKER"])
		}
	}

	if indexOf(installOrder, "node") > indexOf(installOrder, "claude") {
		t.Errorf("node must install before claude-code; install order = %v", installOrder)
	}
}

func TestInstallFeatures_Empty(t *testing.T) {
	_, err := InstallFeatures(context.Background(), &mockDockerClient{}, &mockFeaturePuller{},
		"cid", nil, "user", testLogger(), &strings.Builder{})
	if err != nil {
		t.Errorf("expected nil error for empty features: %v", err)
	}
}

// TestInstallFeatures_ContainerEnvPropagates is the regression test for the bug
// where a feature's containerEnv (e.g. the node feature putting node/npm on PATH)
// never reached the features installed after it, so claude-code could not find
// node. It asserts the env is layered onto later installs and returned for the
// image bake, and that a feature does not yet see its own containerEnv.
func TestInstallFeatures_ContainerEnvPropagates(t *testing.T) {
	mock := &mockDockerClient{}

	first := "ghcr.io/test/first:1"
	second := "ghcr.io/test/second:1"
	puller := &mockFeaturePuller{
		tarData: buildFeatureTar(t, "shared", "1.0.0"),
		metaByRef: map[string]*FeatureMeta{
			// first publishes PATH and a marker var; second depends on first.
			first: {ID: "first", ContainerEnv: map[string]string{
				"PATH":   "/opt/first/bin:${PATH}",
				"FIRSTV": "yes",
			}, Options: map[string]FeatureOption{"marker": {Default: "first"}}},
			second: {ID: "second", InstallsAfter: []string{"ghcr.io/test/first"},
				Options: map[string]FeatureOption{"marker": {Default: "second"}}},
		},
	}
	features := map[string]any{first: map[string]any{}, second: map[string]any{}}

	baked, err := InstallFeatures(context.Background(), mock, puller, "cid",
		features, "vscode", testLogger(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}

	// Returned env (baked into the image) carries first's containerEnv.
	if !strings.Contains(baked["PATH"], "/opt/first/bin") {
		t.Errorf("baked PATH = %q, want it to contain /opt/first/bin", baked["PATH"])
	}
	if baked["FIRSTV"] != "yes" {
		t.Errorf("baked FIRSTV = %q, want yes", baked["FIRSTV"])
	}

	// Map each install.sh run to its feature via MARKER, then check env exposure.
	envByFeature := map[string]map[string]string{}
	for _, c := range mock.execCalls {
		if isInstallRun(c) {
			env := toEnvMap(c.Opts.Env)
			envByFeature[env["MARKER"]] = env
		}
	}

	// second runs after first and must see first's PATH addition + FIRSTV.
	if got := envByFeature["second"]["PATH"]; !strings.Contains(got, "/opt/first/bin") {
		t.Errorf("second's install PATH = %q, want it to contain /opt/first/bin", got)
	}
	if got := envByFeature["second"]["FIRSTV"]; got != "yes" {
		t.Errorf("second's install FIRSTV = %q, want yes", got)
	}
	// first must NOT yet see its own containerEnv (applied only after it installs).
	if got := envByFeature["first"]["FIRSTV"]; got != "" {
		t.Errorf("first should not see its own FIRSTV during install, got %q", got)
	}
}

func TestExpandEnvRefs(t *testing.T) {
	lookup := map[string]string{"PATH": "/usr/bin", "HOME": "/root"}
	cases := []struct{ in, want string }{
		{"/opt/bin:${PATH}", "/opt/bin:/usr/bin"},
		{"$HOME/go", "/root/go"},
		{"literal", "literal"},
		{"${MISSING}/x", "/x"},
	}
	for _, c := range cases {
		if got := expandEnvRefs(c.in, lookup); got != c.want {
			t.Errorf("expandEnvRefs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMapToEnvSlice_Sorted(t *testing.T) {
	got := mapToEnvSlice(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []string{"A=1", "B=2", "C=3"}
	if !equalStrings(got, want) {
		t.Errorf("mapToEnvSlice = %v, want sorted %v", got, want)
	}
}

func TestOrderFeatures_Independent(t *testing.T) {
	metas := map[string]*FeatureMeta{
		"ghcr.io/z/feature:1": {},
		"ghcr.io/a/feature:1": {},
		"ghcr.io/m/feature:1": {},
	}
	order, err := orderFeatures(metas)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ghcr.io/a/feature:1", "ghcr.io/m/feature:1", "ghcr.io/z/feature:1"}
	if !equalStrings(order, want) {
		t.Errorf("order = %v, want alphabetical %v", order, want)
	}
}

func TestOrderFeatures_ReorderByDependency(t *testing.T) {
	// claude-code sorts alphabetically before node but must install after it.
	claude := "ghcr.io/anthropics/devcontainer-features/claude-code:1"
	node := "ghcr.io/devcontainers/features/node:1"
	metas := map[string]*FeatureMeta{
		claude: {InstallsAfter: []string{"ghcr.io/devcontainers/features/node"}},
		node:   {},
	}
	order, err := orderFeatures(metas)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(order, node) > indexOf(order, claude) {
		t.Errorf("expected %s before %s, got %v", node, claude, order)
	}
}

func TestOrderFeatures_TagMismatchMatching(t *testing.T) {
	// Dependency declared untagged (...node) but present tagged (...node:1).
	dependent := "ghcr.io/x/dependent:2"
	node := "ghcr.io/devcontainers/features/node:1"
	metas := map[string]*FeatureMeta{
		dependent: {InstallsAfter: []string{"ghcr.io/devcontainers/features/node"}},
		node:      {},
	}
	order, err := orderFeatures(metas)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(order, node) > indexOf(order, dependent) {
		t.Errorf("untagged edge should resolve to tagged ref; order = %v", order)
	}
}

func TestOrderFeatures_AbsentDependencyIgnored(t *testing.T) {
	a := "ghcr.io/a/feature:1"
	b := "ghcr.io/b/feature:1"
	metas := map[string]*FeatureMeta{
		a: {InstallsAfter: []string{"ghcr.io/not/present"}},
		b: {},
	}
	order, err := orderFeatures(metas)
	if err != nil {
		t.Fatalf("edge to absent feature must be ignored, got error: %v", err)
	}
	want := []string{a, b}
	if !equalStrings(order, want) {
		t.Errorf("order = %v, want deterministic alphabetical %v", order, want)
	}
}

func TestOrderFeatures_Cycle(t *testing.T) {
	a := "ghcr.io/a/feature:1"
	b := "ghcr.io/b/feature:1"
	metas := map[string]*FeatureMeta{
		a: {InstallsAfter: []string{"ghcr.io/b/feature"}},
		b: {InstallsAfter: []string{"ghcr.io/a/feature"}},
	}
	_, err := orderFeatures(metas)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestNormalizeRef(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/devcontainers/features/node:1": "ghcr.io/devcontainers/features/node",
		"ghcr.io/devcontainers/features/node":   "ghcr.io/devcontainers/features/node",
	}
	for in, want := range cases {
		if got := normalizeRef(in); got != want {
			t.Errorf("normalizeRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRef_UnparseableFallback(t *testing.T) {
	// An uppercase repository is invalid for name.ParseReference, exercising the
	// string-strip fallback. The tag must still be dropped so edge matching works.
	cases := map[string]string{
		"REG.io/Org/Repo:7":          "REG.io/Org/Repo",
		"REG.io/Org/Repo@sha256:abc": "REG.io/Org/Repo",
		"REG.io/Org/Repo/":           "REG.io/Org/Repo",
	}
	for in, want := range cases {
		if got := normalizeRef(in); got != want {
			t.Errorf("normalizeRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// mockFeaturePuller returns pre-configured feature content. When metaByRef is
// set, it serves per-ref metadata so multi-feature ordering can be exercised;
// otherwise it falls back to the single fixed tarData/meta.
type mockFeaturePuller struct {
	tarData   []byte
	meta      *FeatureMeta
	metaByRef map[string]*FeatureMeta
	tarByRef  map[string][]byte
	err       error
	pulled    []string
}

func (m *mockFeaturePuller) Pull(_ context.Context, ref string) ([]byte, *FeatureMeta, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	m.pulled = append(m.pulled, ref)
	if m.metaByRef != nil {
		tar := m.tarData
		if t, ok := m.tarByRef[ref]; ok {
			tar = t
		}
		return tar, m.metaByRef[ref], nil
	}
	return m.tarData, m.meta, nil
}

func indexOf(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toEnvMap(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
