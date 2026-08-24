package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunsInContainer_FormatDecides is the SC-4596 regression: the host binary
// is bound over the image's own only when the container can execute it. A
// macOS build masking a working linux one cost every in-container `human`
// call, the proxy CA install, and with it all outbound https.
func TestRunsInContainer_FormatDecides(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		content []byte
		want    bool
	}{
		{"elf", append([]byte{0x7f, 'E', 'L', 'F'}, 0x02, 0x01, 0x01, 0x00), true},
		{"mach-o 64-bit", []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01}, false},
		{"pe", []byte{'M', 'Z', 0x90, 0x00}, false},
		{"shorter than the magic", []byte{0x7f, 'E'}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, tc.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := runsInContainer(path); got != tc.want {
				t.Errorf("runsInContainer(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A binary that cannot be read leaves the image's own in place rather than
// failing the launch.
func TestRunsInContainer_UnreadableBinary(t *testing.T) {
	if runsInContainer(filepath.Join(t.TempDir(), "absent")) {
		t.Error("runsInContainer = true for a missing binary, want false")
	}
}
