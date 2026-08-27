package devcontainer

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testVersion is a plausible released daemon stamp; the cache path carries it.
const testVersion = "0.21.0"

// countingFetcher records the arguments of every fetch and optionally writes a
// binary at the destination, so a test can assert both that a download happened
// and that it happened for the right target — without a network.
type countingFetcher struct {
	calls   int
	version string
	goos    string
	goarch  string
	dest    string
	err     error
	// write, when set, produces the file the real fetcher would have published.
	write func(t *testing.T, dest string)
	t     *testing.T
}

func (f *countingFetcher) fetch(_ context.Context, version, goos, goarch, destPath string, _ io.Writer) error {
	f.calls++
	f.version, f.goos, f.goarch, f.dest = version, goos, goarch, destPath
	if f.err != nil {
		return f.err
	}
	if f.write != nil {
		f.write(f.t, destPath)
	}
	return nil
}

// failingFetcher stands in wherever a test needs the no-binary refusal rather
// than a download.
func failingFetcher(_ context.Context, _, _, _, _ string, _ io.Writer) error {
	return fmt.Errorf("no network")
}

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
	writeELF(t, filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64"), elf.EM_X86_64)

	fetcher := &countingFetcher{t: t, err: fmt.Errorf("must not be called")}
	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times, want 0 — a local binary must short-circuit the download", fetcher.calls)
	}
	if got != projectPath {
		t.Errorf("got %q, want project checkout path %q", got, projectPath)
	}
}

// A cached download for the daemon's own version is used, and no second launch
// pays for the network.
func TestResolveContainerHuman_UsesVersionedCache(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	cachePath := filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64")
	writeELF(t, cachePath, elf.EM_X86_64)

	fetcher := &countingFetcher{t: t, err: fmt.Errorf("must not be called")}
	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if got != cachePath {
		t.Errorf("got %q, want cache path %q", got, cachePath)
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times, want 0 — a cached binary means no network", fetcher.calls)
	}
}

func TestResolveContainerHuman_UsesDaemonExecutableWhenItIsLinuxOfTheArch(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(t.TempDir(), "human")
	writeELF(t, exePath, elf.EM_X86_64)

	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, exePath,
		failingFetcher, io.Discard)
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

			if _, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, "", "",
				failingFetcher, io.Discard); err == nil {
				t.Errorf("expected an error for candidate %q, got nil", tc.name)
			}
		})
	}
}

func TestResolveContainerHuman_ErrorNamesEveryPathSearched(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(t.TempDir(), "daemon-exe")

	_, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, exePath,
		failingFetcher, io.Discard)
	if err == nil {
		t.Fatal("expected an error when no candidate exists")
	}
	msg := err.Error()
	for _, want := range []string{
		filepath.Join(project, "bin", "human-linux-amd64"),
		filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64"),
		exePath,
		"amd64",
		testVersion,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestContainerHumanCandidates_CacheCarriesVersion(t *testing.T) {
	home := t.TempDir()

	got := containerHumanCandidates("amd64", testVersion, "/proj", home, "")

	versioned := filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64")
	stale := filepath.Join(home, ".human", "bin", "human-linux-amd64")
	var sawVersioned bool
	for _, c := range got {
		if c == versioned {
			sawVersioned = true
		}
		if c == stale {
			t.Errorf("candidates still contain the version-less cache path %q", stale)
		}
	}
	if !sawVersioned {
		t.Errorf("candidates = %v, want it to contain %q", got, versioned)
	}
}

// Without a released version there is nothing to name a cache entry after, so
// the home cache is not a candidate at all.
func TestContainerHumanCandidates_NoVersionDropsCache(t *testing.T) {
	home := t.TempDir()

	for _, c := range containerHumanCandidates("amd64", "", "/proj", home, "") {
		if strings.Contains(c, filepath.Join(".human", "bin")) {
			t.Errorf("candidate %q uses the home cache with no version", c)
		}
	}
}

// The stale-after-upgrade case: a daemon at 0.21.0 must never hand the
// container the 0.20.0 binary a previous daemon cached.
func TestResolveContainerHuman_IgnoresOtherVersionCache(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	old := filepath.Join(home, ".human", "bin", "human-0.20.0-linux-amd64")
	writeELF(t, old, elf.EM_X86_64)

	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		failingFetcher, io.Discard)
	if err == nil {
		t.Fatalf("expected an error, got path %q", got)
	}
	if got == old {
		t.Errorf("resolved the previous version's cached binary %q", old)
	}
}

func TestResolveContainerHuman_FetchesWhenNothingLocal(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	fetcher := &countingFetcher{t: t, write: func(t *testing.T, dest string) {
		t.Helper()
		writeELF(t, dest, elf.EM_X86_64)
	}}

	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	want := filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64")
	if got != want {
		t.Errorf("got %q, want the versioned cache path %q", got, want)
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher called %d times, want exactly 1", fetcher.calls)
	}
	if fetcher.version != testVersion || fetcher.goos != "linux" || fetcher.goarch != "amd64" {
		t.Errorf("fetched (%q,%q,%q), want (%q,\"linux\",\"amd64\")",
			fetcher.version, fetcher.goos, fetcher.goarch, testVersion)
	}
	if fetcher.dest != want {
		t.Errorf("fetch destination = %q, want %q", fetcher.dest, want)
	}
}

// The cache holds one binary per architecture, not one per release ever run.
func TestResolveContainerHuman_PrunesOtherVersions(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	stale := filepath.Join(home, ".human", "bin", "human-0.20.0-linux-amd64")
	writeELF(t, stale, elf.EM_X86_64)
	fetcher := &countingFetcher{t: t, write: func(t *testing.T, dest string) {
		t.Helper()
		writeELF(t, dest, elf.EM_X86_64)
	}}

	got, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err != nil {
		t.Fatalf("resolveContainerHuman: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale cache entry %q survived the fetch (stat err = %v)", stale, err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("fetched binary %q missing: %v", got, err)
	}
}

// A source-built daemon stamps "dev": there is no release behind it, so the
// launch must refuse rather than request a URL that cannot exist.
func TestResolveContainerHuman_NoFetchForDevVersion(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	fetcher := &countingFetcher{t: t}

	_, err := resolveContainerHuman(context.Background(), "amd64", "dev", project, home, "",
		fetcher.fetch, io.Discard)
	if err == nil {
		t.Fatal("expected a refusal for an unreleased daemon version")
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times for version dev, want 0", fetcher.calls)
	}
	for _, want := range []string{"dev", "make build-linux"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// A plain `devcontainer up` carries no DaemonInfo, so there is no version to
// match and nothing is downloaded.
func TestResolveContainerHuman_NoFetchWithoutDaemonInfo(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	fetcher := &countingFetcher{t: t}

	_, err := resolveContainerHuman(context.Background(), "amd64", "", project, home, "",
		fetcher.fetch, io.Discard)
	if err == nil {
		t.Fatal("expected a refusal when no daemon version is known")
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times without a daemon version, want 0", fetcher.calls)
	}
	if !strings.Contains(err.Error(), filepath.Join(project, "bin", "human-linux-amd64")) {
		t.Errorf("error %q does not list the searched paths", err.Error())
	}
}

func TestResolveContainerHuman_FetchFailureNamesVersionAndPaths(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	fetcher := &countingFetcher{t: t, err: fmt.Errorf("network down")}

	_, err := resolveContainerHuman(context.Background(), "amd64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err == nil {
		t.Fatal("expected an error when the fetch fails")
	}
	for _, want := range []string{
		testVersion,
		filepath.Join(project, "bin", "human-linux-amd64"),
		filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-amd64"),
		"network down",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// A download of the wrong machine must not become the cache entry every later
// launch reuses.
func TestResolveContainerHuman_DownloadedFileWrongArch(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	fetcher := &countingFetcher{t: t, write: func(t *testing.T, dest string) {
		t.Helper()
		writeELF(t, dest, elf.EM_X86_64)
	}}

	_, err := resolveContainerHuman(context.Background(), "arm64", testVersion, project, home, "",
		fetcher.fetch, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a download of the wrong architecture")
	}
	bad := filepath.Join(home, ".human", "bin", "human-"+testVersion+"-linux-arm64")
	if _, statErr := os.Stat(bad); !os.IsNotExist(statErr) {
		t.Errorf("wrong-architecture download %q was kept (stat err = %v)", bad, statErr)
	}
}

// An interrupted download leaves a temp file behind; pruning takes the old ones
// and leaves a fresh one alone, because a fresh one may be another launch's
// download in flight.
func TestPruneStaleCache_KeepsFreshTempFiles(t *testing.T) {
	dir := t.TempDir()
	keep := "human-0.21.0-linux-amd64"
	writeELF(t, filepath.Join(dir, keep), elf.EM_X86_64)
	writeELF(t, filepath.Join(dir, "human-0.20.0-linux-amd64"), elf.EM_X86_64)
	writeELF(t, filepath.Join(dir, "human-0.20.0-linux-arm64"), elf.EM_AARCH64)

	fresh := filepath.Join(dir, ".human-dl-fresh.tar.gz")
	if err := os.WriteFile(fresh, []byte("in flight"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, ".human-dl-old.tar.gz")
	if err := os.WriteFile(old, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	pruneStaleCache(dir, keep, "amd64")

	for _, gone := range []string{filepath.Join(dir, "human-0.20.0-linux-amd64"), old} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%q survived pruning (stat err = %v)", gone, err)
		}
	}
	for _, kept := range []string{filepath.Join(dir, keep), fresh, filepath.Join(dir, "human-0.20.0-linux-arm64")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%q was pruned: %v", kept, err)
		}
	}
}
