package botidentity

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad_defaultsWhenNoConfig(t *testing.T) {
	id, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Identity{Name: DefaultName, Email: DefaultEmail}
	if id != want {
		t.Errorf("Load = %+v, want %+v", id, want)
	}
}

func TestLoad_readsConfiguredValues(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "bot:\n  name: acmebot\n  email: bot@acme.dev\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Identity{Name: "acmebot", Email: "bot@acme.dev"}
	if id != want {
		t.Errorf("Load = %+v, want %+v", id, want)
	}
}

func TestLoad_fillsMissingFieldWithDefault(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "bot:\n  name: acmebot\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Identity{Name: "acmebot", Email: DefaultEmail}
	if id != want {
		t.Errorf("Load = %+v, want %+v", id, want)
	}
}

func TestGitEnv(t *testing.T) {
	id := Identity{Name: "acmebot", Email: "bot@acme.dev"}
	got := id.GitEnv()
	want := []string{
		"GIT_AUTHOR_NAME=acmebot",
		"GIT_AUTHOR_EMAIL=bot@acme.dev",
		"GIT_COMMITTER_NAME=acmebot",
		"GIT_COMMITTER_EMAIL=bot@acme.dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GitEnv = %v, want %v", got, want)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
}
