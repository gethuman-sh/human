package vieweridentity

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_readsDeclaredNames(t *testing.T) {
	dir := writeConfig(t, "me:\n  names:\n    - Stephan Schmidt\n    - stephanschmidt\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !id.Known() {
		t.Fatal("declared names must produce a known viewer")
	}
	if !id.Matches("Stephan Schmidt") || !id.Matches("stephanschmidt") {
		t.Fatalf("both declared identities must match, got %v", id.Names)
	}
	if id.Matches("André Neubauer") {
		t.Fatal("someone else's name must not match")
	}
}

// An unconfigured project must read as "viewer unknown" rather than as an
// identity that matches nothing — the board dims nothing in the first case and
// would dim EVERY card in the second.
func TestLoad_noSectionIsUnknown(t *testing.T) {
	dir := writeConfig(t, "project: cli\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if id.Known() {
		t.Fatalf("no me section must yield an unknown viewer, got %v", id.Names)
	}
}

func TestLoad_missingFileIsUnknown(t *testing.T) {
	id, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if id.Known() {
		t.Fatalf("missing config must yield an unknown viewer, got %v", id.Names)
	}
}

// A blank or whitespace-only entry is a config typo, not an identity: keeping
// it would make Known() true while matching nothing, which dims the whole board.
func TestLoad_dropsBlankEntries(t *testing.T) {
	dir := writeConfig(t, "me:\n  names:\n    - \"  \"\n    - \"\"\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if id.Known() {
		t.Fatalf("blank entries must not count as an identity, got %v", id.Names)
	}
}

func TestLoad_trimsSurroundingWhitespace(t *testing.T) {
	dir := writeConfig(t, "me:\n  names:\n    - \"  Stephan Schmidt  \"\n")

	id, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !id.Matches("Stephan Schmidt") {
		t.Fatalf("a padded config entry must still match, got %v", id.Names)
	}
}

func TestMatches(t *testing.T) {
	id := Identity{Names: []string{"Alice", "alice-gh"}}

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "exact", input: "Alice", want: true},
		{name: "secondIdentity", input: "alice-gh", want: true},
		{name: "differentCase", input: "ALICE-GH", want: true},
		{name: "paddedInput", input: "  Alice  ", want: true},
		{name: "otherPerson", input: "Bob", want: false},
		{name: "empty", input: "", want: false},
		{name: "whitespaceOnly", input: "   ", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := id.Matches(tc.input); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestKnown_zeroValue(t *testing.T) {
	if (Identity{}).Known() {
		t.Fatal("the zero identity must never be known")
	}
}
