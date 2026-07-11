package starter

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates the given relative file paths (with parent dirs) under
// root, each with trivial content.
func writeTree(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIsEmptyProject(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{name: "empty dir", files: nil, want: true},
		{
			name: "config only",
			files: []string{
				".humanconfig.yaml", "CLAUDE.md", "README.md", ".gitignore",
				".git/HEAD", ".claude/settings.json",
				".devcontainer/devcontainer.json", ".vscode/settings.json",
				"FEATURE.json",
			},
			want: true,
		},
		{name: "go.mod only", files: []string{"go.mod"}, want: false},
		{name: "root source file", files: []string{"main.go"}, want: false},
		{name: "nested source file", files: []string{"src/index.js"}, want: false},
		{name: "uppercase extension", files: []string{"Main.GO"}, want: false},
		{
			name:  "source only under skip dir",
			files: []string{"node_modules/lib/index.js", "vendor/pkg/mod.go"},
			want:  true,
		},
		{
			name:  "source only under dot dir",
			files: []string{".claude/hooks/check.py"},
			want:  true,
		},
		{name: "manifest in subdirectory", files: []string{"backend/package.json"}, want: false},
		{name: "makefile counts as project", files: []string{"Makefile"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, tc.files...)
			got, err := IsEmptyProject(dir)
			if err != nil {
				t.Fatalf("IsEmptyProject: %v", err)
			}
			if got != tc.want {
				t.Fatalf("IsEmptyProject = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsEmptyProjectMissingDir(t *testing.T) {
	if _, err := IsEmptyProject(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}
