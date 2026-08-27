package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// elfBytes is a minimal but well-formed ELF64 header, so the extracted file is
// a plausible binary rather than arbitrary bytes (mirrors the fixture
// internal/devcontainer's tests use).
func elfBytes(machine elf.Machine) []byte {
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0})
	binary.LittleEndian.PutUint16(b[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(b[18:], uint16(machine))
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}

// buildArchive produces a tar.gz with the layout the real release ships:
// LICENSE, README.md and human, no directory prefix.
func buildArchive(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// serveRelease stands in for the releases host: it serves the given paths
// (relative to the download root) and 404s everything else, counting requests
// so a test can assert that no request was made at all.
func serveRelease(t *testing.T, files map[string][]byte) *int {
	t.Helper()
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	prev := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = prev })
	return &requests
}

// releaseFiles is the archive plus a checksums.txt in goreleaser's format.
func releaseFiles(version, goos, goarch string, archive []byte) map[string][]byte {
	name := ArchiveName(version, goos, goarch)
	checksums := fmt.Sprintf("%s  %s\n%s  human_%s_darwin_arm64.tar.gz\n",
		sha256Hex(archive), name, sha256Hex([]byte("other")), version)
	return map[string][]byte{
		"v" + version + "/" + name:       archive,
		"v" + version + "/checksums.txt": []byte(checksums),
	}
}

func TestVersion_StripsPrefixAndRejectsDev(t *testing.T) {
	for _, in := range []string{"0.21.0", "v0.21.0", " v0.21.0 "} {
		got, err := Version(in)
		if err != nil {
			t.Errorf("Version(%q): %v", in, err)
		}
		if got != "0.21.0" {
			t.Errorf("Version(%q) = %q, want 0.21.0", in, got)
		}
	}
	for _, in := range []string{"", "dev", "nightly", "v"} {
		if _, err := Version(in); !stderrors.Is(err, ErrNoRelease) {
			t.Errorf("Version(%q) error = %v, want ErrNoRelease", in, err)
		}
	}
}

func TestArchiveName(t *testing.T) {
	if got := ArchiveName("0.21.0", "linux", "arm64"); got != "human_0.21.0_linux_arm64.tar.gz" {
		t.Errorf("ArchiveName = %q", got)
	}
}

func TestFetchBinary_Success(t *testing.T) {
	binary := elfBytes(elf.EM_AARCH64)
	archive := buildArchive(t, map[string][]byte{
		"LICENSE": []byte("license"), "README.md": []byte("readme"), "human": binary,
	})
	serveRelease(t, releaseFiles("0.21.0", "linux", "arm64", archive))

	dest := filepath.Join(t.TempDir(), "cache", "human-0.21.0-linux-arm64")
	var progress bytes.Buffer
	if err := FetchBinary(context.Background(), "v0.21.0", "linux", "arm64", dest, &progress); err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}

	got, err := os.ReadFile(dest) // #nosec G304 -- a path this test composed
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Error("published file does not match the archive's human entry")
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", fi.Mode().Perm())
	}
	for _, want := range []string{"0.21.0", "linux/arm64"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress %q does not mention %q", progress.String(), want)
		}
	}
	assertNoLeftovers(t, filepath.Dir(dest), filepath.Base(dest))
}

// A nil progress writer is the caller saying "say nothing", not a crash.
func TestFetchBinary_NilProgress(t *testing.T) {
	archive := buildArchive(t, map[string][]byte{"human": elfBytes(elf.EM_X86_64)})
	serveRelease(t, releaseFiles("0.21.0", "linux", "amd64", archive))

	dest := filepath.Join(t.TempDir(), "human-0.21.0-linux-amd64")
	if err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", dest, nil); err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
}

func TestFetchBinary_ChecksumMismatch(t *testing.T) {
	archive := buildArchive(t, map[string][]byte{"human": elfBytes(elf.EM_X86_64)})
	name := ArchiveName("0.21.0", "linux", "amd64")
	serveRelease(t, map[string][]byte{
		"v0.21.0/" + name:       archive,
		"v0.21.0/checksums.txt": []byte(sha256Hex([]byte("something else")) + "  " + name + "\n"),
	})

	dir := t.TempDir()
	dest := filepath.Join(dir, "human-0.21.0-linux-amd64")
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", dest, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("an unverified archive left %q behind (stat err = %v)", dest, statErr)
	}
	assertNoLeftovers(t, dir, "")
}

func TestFetchBinary_ArchiveNotInChecksums(t *testing.T) {
	serveRelease(t, map[string][]byte{
		"v0.21.0/checksums.txt": []byte(sha256Hex([]byte("x")) + "  human_0.21.0_darwin_arm64.tar.gz\n"),
	})

	dir := t.TempDir()
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(dir, "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "publishes no such archive") {
		t.Fatalf("err = %v, want 'publishes no such archive'", err)
	}
	assertNoLeftovers(t, dir, "")
}

func TestFetchBinary_ChecksumsMissing(t *testing.T) {
	serveRelease(t, map[string][]byte{})

	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(t.TempDir(), "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "checksums request failed") {
		t.Fatalf("err = %v, want the checksums request to be named", err)
	}
}

func TestFetchBinary_Archive404(t *testing.T) {
	archive := buildArchive(t, map[string][]byte{"human": elfBytes(elf.EM_X86_64)})
	files := releaseFiles("0.21.0", "linux", "amd64", archive)
	delete(files, "v0.21.0/"+ArchiveName("0.21.0", "linux", "amd64"))
	serveRelease(t, files)

	dir := t.TempDir()
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(dir, "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the HTTP status named", err)
	}
	assertNoLeftovers(t, dir, "")
}

func TestFetchBinary_NoHumanEntry(t *testing.T) {
	archive := buildArchive(t, map[string][]byte{"LICENSE": []byte("license")})
	serveRelease(t, releaseFiles("0.21.0", "linux", "amd64", archive))

	dir := t.TempDir()
	dest := filepath.Join(dir, "human-0.21.0-linux-amd64")
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", dest, nil)
	if err == nil || !strings.Contains(err.Error(), "contains no human binary") {
		t.Fatalf("err = %v, want 'contains no human binary'", err)
	}
	assertNoLeftovers(t, dir, "")
}

func TestFetchBinary_NotAnArchive(t *testing.T) {
	body := []byte("<html>not a tarball</html>")
	name := ArchiveName("0.21.0", "linux", "amd64")
	serveRelease(t, map[string][]byte{
		"v0.21.0/" + name:       body,
		"v0.21.0/checksums.txt": []byte(sha256Hex(body) + "  " + name + "\n"),
	})

	dir := t.TempDir()
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(dir, "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "decompressing") {
		t.Fatalf("err = %v, want a decompression failure", err)
	}
	assertNoLeftovers(t, dir, "")
}

// An unreleased build stamp must not produce a request for a URL that could
// never exist.
func TestFetchBinary_UnreleasedVersion(t *testing.T) {
	requests := serveRelease(t, map[string][]byte{})

	err := FetchBinary(context.Background(), "dev", "linux", "amd64", filepath.Join(t.TempDir(), "human"), nil)
	if !stderrors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v, want ErrNoRelease", err)
	}
	if *requests != 0 {
		t.Errorf("made %d HTTP requests for an unreleased version, want 0", *requests)
	}
}

func TestFetchBinary_Windows(t *testing.T) {
	requests := serveRelease(t, map[string][]byte{})

	err := FetchBinary(context.Background(), "0.21.0", "windows", "amd64", filepath.Join(t.TempDir(), "human.exe"), nil)
	if err == nil || !strings.Contains(err.Error(), "zip") {
		t.Fatalf("err = %v, want the zip limitation named", err)
	}
	if *requests != 0 {
		t.Errorf("made %d HTTP requests for an unsupported target, want 0", *requests)
	}
}

// A body larger than the cap is a corrupt or hostile response, not a release.
func TestFetchBinary_OversizeArchive(t *testing.T) {
	prev := httpGet
	t.Cleanup(func() { httpGet = prev })
	name := ArchiveName("0.21.0", "linux", "amd64")
	httpGet = func(_ context.Context, url string) (*http.Response, error) {
		if strings.HasSuffix(url, "checksums.txt") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("a", 64) + "  " + name + "\n")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(&endlessReader{}),
		}, nil
	}

	dir := t.TempDir()
	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(dir, "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err = %v, want the size limit named", err)
	}
	assertNoLeftovers(t, dir, "")
}

// endlessReader never ends, so the download runs into its cap.
type endlessReader struct{}

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestFetchBinary_TransportError(t *testing.T) {
	prev := httpGet
	t.Cleanup(func() { httpGet = prev })
	httpGet = func(context.Context, string) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: no route to host")
	}

	err := FetchBinary(context.Background(), "0.21.0", "linux", "amd64", filepath.Join(t.TempDir(), "human"), nil)
	if err == nil || !strings.Contains(err.Error(), "checksums") {
		t.Fatalf("err = %v, want the failing step named", err)
	}
}

// assertNoLeftovers fails when the cache directory holds anything but keep —
// a partial download that survives would be reused by the next launch.
func assertNoLeftovers(t *testing.T, dir, keep string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != keep {
			t.Errorf("leftover file %q in %q", e.Name(), dir)
		}
	}
}
