package devcontainer

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeELF writes a minimal but well-formed ELF64 header for machine, so a test
// can exercise architecture selection without shipping real binaries.
func writeELF(t *testing.T, path string, machine elf.Machine) {
	t.Helper()
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}) // ELF64, little-endian, current version
	binary.LittleEndian.PutUint16(b[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(b[18:], uint16(machine))
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeFakeLinuxHuman puts a usable candidate where a launch for arch looks first.
func writeFakeLinuxHuman(t *testing.T, projectDir, arch string) string {
	t.Helper()
	path := filepath.Join(projectDir, "bin", "human-linux-"+arch)
	writeELF(t, path, elfMachineFor(arch))
	return path
}

func TestResolveContainerHuman_PrefersProjectCheckout(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	projectPath := writeFakeLinuxHuman(t, project, "amd64")
	writeELF(t, filepath.Join(home, ".human", "bin", "human-linux-amd64"), elf.EM_X86_64)

	got, err := resolveContainerHuman("amd64", project, home, "")
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if got != projectPath {
		t.Errorf("got %q, want project checkout path %q", got, projectPath)
	}
}

func TestResolveContainerHuman_FallsBackToHumanBinCache(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	cachePath := filepath.Join(home, ".human", "bin", "human-linux-amd64")
	writeELF(t, cachePath, elf.EM_X86_64)

	got, err := resolveContainerHuman("amd64", project, home, "")
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if got != cachePath {
		t.Errorf("got %q, want cache path %q", got, cachePath)
	}
}

func TestResolveContainerHuman_UsesDaemonExecutableWhenItIsLinuxOfTheArch(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(t.TempDir(), "human")
	writeELF(t, exePath, elf.EM_X86_64)

	got, err := resolveContainerHuman("amd64", project, home, exePath)
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if got != exePath {
		t.Errorf("got %q, want daemon executable %q", got, exePath)
	}
}

func TestResolveContainerHuman_RejectsWrongFormatAndWrongArch(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{
			name: "macho",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				// Mach-O 64-bit magic, the format the reported bug actually shipped.
				if err := os.WriteFile(path, []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "pe",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte{'M', 'Z', 0, 0, 0, 0, 0, 0}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "truncated",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte{0x7f, 'E'}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "empty",
			write: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong-arch",
			write: func(t *testing.T, path string) {
				t.Helper()
				writeELF(t, path, elf.EM_AARCH64)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			path := filepath.Join(project, "bin", "human-linux-amd64")
			tc.write(t, path)

			if _, err := resolveContainerHuman("amd64", project, "", ""); err == nil {
				t.Errorf("expected an error for candidate %q, got nil", tc.name)
			}
		})
	}
}

func TestResolveContainerHuman_ErrorNamesEveryPathSearched(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(t.TempDir(), "daemon-exe")

	_, err := resolveContainerHuman("amd64", project, home, exePath)
	if err == nil {
		t.Fatal("expected an error when no candidate exists")
	}
	msg := err.Error()
	for _, want := range []string{
		filepath.Join(project, "bin", "human-linux-amd64"),
		filepath.Join(home, ".human", "bin", "human-linux-amd64"),
		exePath,
		"amd64",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}
