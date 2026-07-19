package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONC_LineComments(t *testing.T) {
	input := []byte(`{
  // This is a comment
  "name": "test"
}`)
	got := string(StripJSONC(input))
	if contains(got, "//") {
		t.Errorf("line comment not stripped: %s", got)
	}
	if !contains(got, `"name"`) {
		t.Errorf("content lost: %s", got)
	}
}

func TestStripJSONC_BlockComments(t *testing.T) {
	input := []byte(`{
  /* block comment */
  "name": "test"
}`)
	got := string(StripJSONC(input))
	if contains(got, "/*") || contains(got, "*/") {
		t.Errorf("block comment not stripped: %s", got)
	}
	if !contains(got, `"name"`) {
		t.Errorf("content lost: %s", got)
	}
}

func TestStripJSONC_PreservesStrings(t *testing.T) {
	input := []byte(`{"url": "https://example.com // not a comment"}`)
	got := string(StripJSONC(input))
	if !contains(got, "// not a comment") {
		t.Errorf("string content was stripped: %s", got)
	}
}

func TestStripJSONC_EscapedQuotes(t *testing.T) {
	input := []byte(`{"val": "escaped \" quote // still string"}`)
	got := string(StripJSONC(input))
	if !contains(got, "// still string") {
		t.Errorf("string content was stripped after escaped quote: %s", got)
	}
}

func TestParseConfig_ImageBased(t *testing.T) {
	data := []byte(`{
  // Image-based config
  "name": "test container",
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": {
    "ghcr.io/devcontainers/features/node:1": {"version": "22"}
  },
  "remoteEnv": {"FOO": "bar"},
  "remoteUser": "vscode"
}`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "test container" {
		t.Errorf("name = %q, want %q", cfg.Name, "test container")
	}
	if cfg.Image != "mcr.microsoft.com/devcontainers/base:ubuntu" {
		t.Errorf("image = %q", cfg.Image)
	}
	if len(cfg.Features) != 1 {
		t.Errorf("features len = %d, want 1", len(cfg.Features))
	}
	if cfg.RemoteUser != "vscode" {
		t.Errorf("remoteUser = %q", cfg.RemoteUser)
	}
	if cfg.RemoteEnv["FOO"] != "bar" {
		t.Errorf("remoteEnv[FOO] = %q", cfg.RemoteEnv["FOO"])
	}
}

func TestParseConfig_DockerfileBased(t *testing.T) {
	data := []byte(`{
  "build": {
    "dockerfile": "Dockerfile",
    "context": "..",
    "args": {"VARIANT": "3.9"}
  }
}`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Build == nil {
		t.Fatal("build is nil")
	}
	if cfg.Build.Dockerfile != "Dockerfile" {
		t.Errorf("dockerfile = %q", cfg.Build.Dockerfile)
	}
	if cfg.Build.Context != ".." {
		t.Errorf("context = %q", cfg.Build.Context)
	}
	if cfg.Build.Args["VARIANT"] != "3.9" {
		t.Errorf("args[VARIANT] = %q", cfg.Build.Args["VARIANT"])
	}
}

func TestParseConfig_LifecycleCommands(t *testing.T) {
	data := []byte(`{
  "image": "ubuntu",
  "postStartCommand": "echo hello",
  "onCreateCommand": ["npm", "install"],
  "postCreateCommand": {"setup": "make setup", "lint": "make lint"}
}`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}

	// String command.
	if s, ok := cfg.PostStartCommand.(string); !ok || s != "echo hello" {
		t.Errorf("postStartCommand = %v", cfg.PostStartCommand)
	}

	// Array command.
	if arr, ok := cfg.OnCreateCommand.([]any); !ok || len(arr) != 2 {
		t.Errorf("onCreateCommand = %v", cfg.OnCreateCommand)
	}

	// Map command (parallel).
	if m, ok := cfg.PostCreateCommand.(map[string]any); !ok || len(m) != 2 {
		t.Errorf("postCreateCommand = %v", cfg.PostCreateCommand)
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := ParseConfig([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestFindConfig_Standard(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := FindConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(path)) != ".devcontainer" {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestFindConfig_RootLevel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := FindConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != ".devcontainer.json" {
		t.Errorf("unexpected path: %s", path)
	}
}

func TestFindConfig_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindConfig(dir)
	if err == nil {
		t.Error("expected error when config not found")
	}
}

func TestResolveVariables_LocalEnv(t *testing.T) {
	t.Setenv("TEST_VAR_RESOLVE", "resolved-value")

	cfg := &DevcontainerConfig{
		RemoteEnv: map[string]string{
			"MY_VAR": "${localEnv:TEST_VAR_RESOLVE}",
		},
	}
	resolved := ResolveVariables(cfg, "/tmp/project")
	if resolved.RemoteEnv["MY_VAR"] != "resolved-value" {
		t.Errorf("MY_VAR = %q, want %q", resolved.RemoteEnv["MY_VAR"], "resolved-value")
	}
}

func TestResolveVariables_LocalEnvDefault(t *testing.T) {
	// Ensure the variable does not exist.
	t.Setenv("NONEXISTENT_VAR_FOR_TEST", "")
	os.Unsetenv("NONEXISTENT_VAR_FOR_TEST") //nolint:errcheck // test cleanup

	cfg := &DevcontainerConfig{
		RemoteEnv: map[string]string{
			"MY_VAR": "${localEnv:NONEXISTENT_VAR_FOR_TEST:fallback}",
		},
	}
	resolved := ResolveVariables(cfg, "/tmp/project")
	if resolved.RemoteEnv["MY_VAR"] != "fallback" {
		t.Errorf("MY_VAR = %q, want %q", resolved.RemoteEnv["MY_VAR"], "fallback")
	}
}

func TestResolveVariables_WorkspaceFolder(t *testing.T) {
	cfg := &DevcontainerConfig{
		WorkspaceFolder: "/workspaces/${localWorkspaceFolderBasename}",
	}
	resolved := ResolveVariables(cfg, "/home/user/my-project")
	if resolved.WorkspaceFolder != "/workspaces/my-project" {
		t.Errorf("workspaceFolder = %q", resolved.WorkspaceFolder)
	}
}

func TestResolveVariables_MountStrings(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")

	cfg := &DevcontainerConfig{
		Mounts: []any{
			"source=${localEnv:HOME}/.human/ca.crt,target=/tmp/ca.crt,type=bind",
			42, // non-string entries should be left alone
		},
	}
	resolved := ResolveVariables(cfg, "/tmp/project")
	s, ok := resolved.Mounts[0].(string)
	if !ok {
		t.Fatalf("mount[0] is not string: %T", resolved.Mounts[0])
	}
	if !contains(s, "/home/testuser/.human/ca.crt") {
		t.Errorf("mount[0] = %q, expected resolved HOME", s)
	}
	// Non-string preserved.
	if _, ok := resolved.Mounts[1].(int); !ok {
		t.Errorf("mount[1] should be int, got %T", resolved.Mounts[1])
	}
}

func TestStripJSONC_UnterminatedBlockComment(t *testing.T) {
	input := []byte(`{
  /* unterminated block comment
  "name": "test"
}`)
	got := string(StripJSONC(input))
	// Everything after /* should be consumed.
	if containsStr(got, "unterminated") {
		t.Errorf("unterminated block comment not consumed: %s", got)
	}
	if containsStr(got, `"name"`) {
		t.Errorf("content after unterminated comment should be consumed: %s", got)
	}
}

func TestStripJSONC_UnterminatedString(t *testing.T) {
	// A string that never closes should be copied to end of input.
	input := []byte(`{"key": "unclosed string`)
	got := string(StripJSONC(input))
	if !containsStr(got, "unclosed string") {
		t.Errorf("unclosed string content should be preserved: %s", got)
	}
}

func TestStripJSONC_MultipleCommentTypes(t *testing.T) {
	input := []byte(`{
  // line comment 1
  "a": 1, /* block comment */
  // line comment 2
  "b": 2
}`)
	got := string(StripJSONC(input))
	if containsStr(got, "//") || containsStr(got, "/*") || containsStr(got, "*/") {
		t.Errorf("comments not fully stripped: %s", got)
	}
	if !containsStr(got, `"a"`) || !containsStr(got, `"b"`) {
		t.Errorf("content lost: %s", got)
	}
}

func TestStripJSONC_TrailingCommas(t *testing.T) {
	input := []byte(`{
  "a": 1, // trailing comma is fine in JSONC
  "b": 2,
}`)
	got := string(StripJSONC(input))
	// Comments should be removed but trailing commas preserved (they are
	// not part of JSONC stripping, just comment removal).
	if containsStr(got, "//") {
		t.Errorf("comment not stripped: %s", got)
	}
}

func TestReplaceLocalEnv_NoClosingBrace(t *testing.T) {
	// If there's no closing }, replaceLocalEnv should return what it has.
	input := "${localEnv:MISSING_BRACE"
	got := replaceLocalEnv(input)
	if got != input {
		t.Errorf("replaceLocalEnv(%q) = %q, want input unchanged", input, got)
	}
}

func TestReplaceLocalEnv_Multiple(t *testing.T) {
	t.Setenv("VAR_A", "hello")
	t.Setenv("VAR_B", "world")

	input := "${localEnv:VAR_A} ${localEnv:VAR_B}"
	got := replaceLocalEnv(input)
	if got != "hello world" {
		t.Errorf("replaceLocalEnv(%q) = %q, want %q", input, got, "hello world")
	}
}

func TestReplaceLocalEnv_UnsetNoDefault(t *testing.T) {
	os.Unsetenv("TOTALLY_UNSET_VAR_XYZ") //nolint:errcheck
	input := "prefix-${localEnv:TOTALLY_UNSET_VAR_XYZ}-suffix"
	got := replaceLocalEnv(input)
	if got != "prefix--suffix" {
		t.Errorf("replaceLocalEnv(%q) = %q, want %q", input, got, "prefix--suffix")
	}
}

func TestResolveVariables_Image(t *testing.T) {
	t.Setenv("MY_REGISTRY", "ghcr.io")
	cfg := &DevcontainerConfig{
		Image: "${localEnv:MY_REGISTRY}/myimage:latest",
	}
	resolved := ResolveVariables(cfg, "/tmp/project")
	if resolved.Image != "ghcr.io/myimage:latest" {
		t.Errorf("Image = %q, want %q", resolved.Image, "ghcr.io/myimage:latest")
	}
}

func TestResolveVariables_LocalWorkspaceFolder(t *testing.T) {
	cfg := &DevcontainerConfig{
		ContainerEnv: map[string]string{
			"PROJECT": "${localWorkspaceFolder}",
		},
	}
	resolved := ResolveVariables(cfg, "/home/user/my-project")
	absDir, _ := filepath.Abs("/home/user/my-project")
	if resolved.ContainerEnv["PROJECT"] != absDir {
		t.Errorf("PROJECT = %q, want %q", resolved.ContainerEnv["PROJECT"], absDir)
	}
}

func TestResolveVariables_NilMaps(t *testing.T) {
	cfg := &DevcontainerConfig{}
	resolved := ResolveVariables(cfg, "/tmp")
	if resolved.RemoteEnv != nil {
		t.Errorf("RemoteEnv should remain nil, got %v", resolved.RemoteEnv)
	}
	if resolved.ContainerEnv != nil {
		t.Errorf("ContainerEnv should remain nil, got %v", resolved.ContainerEnv)
	}
}

func TestParseConfig_AllFields(t *testing.T) {
	data := []byte(`{
  "name": "full",
  "image": "ubuntu",
  "remoteUser": "dev",
  "containerUser": "root",
  "workspaceFolder": "/workspace",
  "capAdd": ["SYS_PTRACE"],
  "securityOpt": ["seccomp=unconfined"],
  "privileged": true,
  "runArgs": ["--network=host"],
  "forwardPorts": [8080, "9090:9090"],
  "mounts": ["source=/tmp,target=/mnt,type=bind"]
}`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContainerUser != "root" {
		t.Errorf("containerUser = %q", cfg.ContainerUser)
	}
	if cfg.WorkspaceFolder != "/workspace" {
		t.Errorf("workspaceFolder = %q", cfg.WorkspaceFolder)
	}
	if len(cfg.CapAdd) != 1 || cfg.CapAdd[0] != "SYS_PTRACE" {
		t.Errorf("capAdd = %v", cfg.CapAdd)
	}
	if !cfg.Privileged {
		t.Error("expected Privileged = true")
	}
	if len(cfg.RunArgs) != 1 {
		t.Errorf("runArgs = %v", cfg.RunArgs)
	}
	if len(cfg.ForwardPorts) != 2 {
		t.Errorf("forwardPorts = %v", cfg.ForwardPorts)
	}
	if len(cfg.Mounts) != 1 {
		t.Errorf("mounts = %v", cfg.Mounts)
	}
}

func TestFindConfig_PrefersStandardOverRoot(t *testing.T) {
	dir := t.TempDir()
	// Create both .devcontainer/devcontainer.json and .devcontainer.json
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := FindConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Should prefer .devcontainer/devcontainer.json.
	if filepath.Base(filepath.Dir(path)) != ".devcontainer" {
		t.Errorf("expected .devcontainer/devcontainer.json to be preferred, got %s", path)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && containsStr(haystack, needle)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCacheVolume_Valid(t *testing.T) {
	cases := []struct {
		name  string
		cv    CacheVolume
		valid bool
	}{
		{"go pair", CacheVolume{Name: "go-build", Path: "/home/vscode/.cache/go-build"}, true},
		{"dots and underscore", CacheVolume{Name: "npm_v10.x", Path: "/root/.npm"}, true},
		{"relative path", CacheVolume{Name: "good", Path: "cache"}, false},
		{"slash in name", CacheVolume{Name: "../escape", Path: "/data"}, false},
		{"empty name", CacheVolume{Name: "", Path: "/data"}, false},
		{"leading dash", CacheVolume{Name: "-x", Path: "/data"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cv.Valid(); got != c.valid {
				t.Fatalf("Valid() = %v, want %v", got, c.valid)
			}
		})
	}
}

func TestLoadCaches(t *testing.T) {
	dir := t.TempDir()
	content := "caches:\n  - name: go-build\n    path: /home/vscode/.cache/go-build\n"
	if err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	caches, err := LoadCaches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 1 || caches[0].VolumeName() != "human-cache-go-build" || caches[0].Path != "/home/vscode/.cache/go-build" {
		t.Fatalf("unexpected caches: %+v", caches)
	}
}

func TestLoadCaches_AbsentIsEmpty(t *testing.T) {
	caches, err := LoadCaches(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 0 {
		t.Fatalf("want empty, got %+v", caches)
	}
}
