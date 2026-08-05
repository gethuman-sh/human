package appearance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/settings"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_readsDeclaredPercent(t *testing.T) {
	dir := writeConfig(t, "ui:\n  dim_percent: 20\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Declared() {
		t.Fatal("a declared percent must read as declared")
	}
	if got := a.DimPercent(); got != 20 {
		t.Fatalf("DimPercent() = %d, want 20", got)
	}
}

func TestLoad_missingFileIsUndeclared(t *testing.T) {
	a, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.Declared() {
		t.Fatal("a project with no config file must declare nothing")
	}
	if got := a.DimPercent(); got != 0 {
		t.Fatalf("DimPercent() = %d, want 0", got)
	}
}

func TestLoad_noSectionIsUndeclared(t *testing.T) {
	dir := writeConfig(t, "project: cli\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Declared() {
		t.Fatal("a config without a ui section must declare nothing")
	}
}

func TestLoad_emptySectionIsUndeclared(t *testing.T) {
	dir := writeConfig(t, "ui:\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Declared() {
		t.Fatal("a bare ui: section must declare nothing")
	}
}

func TestDimPercent_rejectsOutOfRange(t *testing.T) {
	// Out of range falls back rather than clamping: the shipped default is a
	// value someone chose, a clamped bound is one nobody did.
	for _, v := range []int{0, -1, 4, 101, 1000} {
		if got := (Appearance{Dim: v}).DimPercent(); got != 0 {
			t.Fatalf("DimPercent() for Dim=%d = %d, want 0", v, got)
		}
	}
}

func TestDimPercent_acceptsBounds(t *testing.T) {
	if got := (Appearance{Dim: MinDimPercent}).DimPercent(); got != 5 {
		t.Fatalf("DimPercent() at the minimum = %d, want 5", got)
	}
	if got := (Appearance{Dim: MaxDimPercent}).DimPercent(); got != 100 {
		t.Fatalf("DimPercent() at the maximum = %d, want 100", got)
	}
}

// Viper decodes weakly, so a quoted number is still a number. The quoting has
// no leading zero on purpose: the weak string->int path parses with base 0, so
// "035" would silently be octal.
func TestLoad_quotedNumberIsAccepted(t *testing.T) {
	dir := writeConfig(t, "ui:\n  dim_percent: \"20\"\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.DimPercent(); got != 20 {
		t.Fatalf("DimPercent() = %d, want 20", got)
	}
}

// Someone who types the CSS opacity instead of the percent must land on the
// shipped default, never on an invisible board: mapstructure truncates 0.35 to
// 0 without erroring, and 0 is the "say nothing" answer.
func TestLoad_floatOpacityFallsBack(t *testing.T) {
	dir := writeConfig(t, "ui:\n  dim_percent: 0.35\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatalf("a truncating decode must not error: %v", err)
	}
	if a.Declared() {
		t.Fatalf("a truncated float must declare nothing, got %d", a.DimPercent())
	}
}

func TestLoad_emptyValueIsUndeclared(t *testing.T) {
	dir := writeConfig(t, "ui:\n  dim_percent: \"\"\n")

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Declared() {
		t.Fatalf("an empty value must declare nothing, got %d", a.DimPercent())
	}
}

// The error is reported, but the returned value must still be safe to use: a
// caller that ignores it lands on the default rather than on a blank board.
func TestLoad_unparseableValueReturnsError(t *testing.T) {
	dir := writeConfig(t, "ui:\n  dim_percent: not-a-number\n")

	a, err := Load(dir)
	if err == nil {
		t.Fatal("an unparseable value must report an error")
	}
	if a.Declared() {
		t.Fatalf("a failed load must declare nothing, got %d", a.DimPercent())
	}
}

// The settings page writes the value and the board reads it back. Nothing else
// pins the two halves to the same key and the same type, so a rename on either
// side would otherwise ship a setting that saves fine and changes nothing.
func TestLoad_readsWhatTheSettingsPageWrites(t *testing.T) {
	dir := t.TempDir()
	if err := settings.SetValue(dir, "ui.dim_percent", 25); err != nil {
		t.Fatal(err)
	}

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.DimPercent(); got != 25 {
		t.Fatalf("DimPercent() = %d, want the 25 the settings page saved", got)
	}
}

// Clearing the row is the only way back to the shipped default, so the write
// path must accept 0 and the read path must treat it as "declared nothing".
func TestLoad_settingsRowClearedReturnsToDefault(t *testing.T) {
	dir := t.TempDir()
	if err := settings.SetValue(dir, "ui.dim_percent", 25); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetValue(dir, "ui.dim_percent", 0); err != nil {
		t.Fatalf("clearing the row must be accepted: %v", err)
	}

	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Declared() {
		t.Fatalf("a cleared row must declare nothing, got %d", a.DimPercent())
	}
}
