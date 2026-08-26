// Package release fetches published human release artifacts for an exact
// version. It lives apart from its callers because "obtain the binary for
// exactly this version, verified" is needed both by an agent container launch
// (SC-4633) and by self-update (SC-1925), and neither may depend on the other —
// an update path must not drag in Docker.
package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/gethuman-sh/human/errors"
)

const (
	// defaultBaseURL is the release download root. The tag directory is
	// "v"+version even though the binary's own build stamp carries no "v"
	// (.goreleaser.yaml stamps main.version={{.Version}}).
	defaultBaseURL = "https://github.com/gethuman-sh/human/releases/download"

	// checksumsName is the file goreleaser publishes beside the archives.
	checksumsName = "checksums.txt"

	// binaryEntry is the archive entry the CLI ships as.
	binaryEntry = "human"

	// Caps guard a corrupt or hostile response (gosec G110 decompression-bomb
	// guard). The 0.20.0 linux archive is ~28 MB compressed and the binary
	// inside it ~50 MB, so these leave room for growth without leaving the
	// door open.
	maxArchiveBytes  = 200 << 20
	maxBinaryBytes   = 300 << 20
	maxChecksumBytes = 1 << 20
)

// baseURL is a var so a test can point the fetch at an httptest server; no test
// ever reaches the real releases host.
var baseURL = defaultBaseURL

// fetchHTTPClient bounds the download so a black-holed connection cannot hang a
// container launch forever.
var fetchHTTPClient = &http.Client{Timeout: 5 * time.Minute}

// httpGet is the HTTP entry point — replaced with a stub in tests.
var httpGet = func(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return fetchHTTPClient.Do(req)
}

// ErrNoRelease reports a build stamp that cannot have a release behind it — a
// source build stamps "dev" (Makefile) and an unstamped build carries nothing.
// Callers distinguish it so they can say "build one" instead of "download
// failed" for a URL that could never have existed.
var ErrNoRelease = stderrors.New("not a published release version")

// Version normalizes a build stamp to the release number a download path is
// built from, or reports ErrNoRelease. The trimmed stamp is returned verbatim
// rather than semver's canonical form: a tag published as "0.21" names its
// artifacts "human_0.21_linux_amd64.tar.gz", and canonicalizing to "0.21.0"
// would request a file that does not exist.
func Version(stamp string) (string, error) {
	num := strings.TrimPrefix(strings.TrimSpace(stamp), "v")
	if num == "" || num == "dev" {
		return "", errors.WrapWithDetails(ErrNoRelease, "build stamp %q has no release behind it", "stamp", stamp)
	}
	if _, err := semver.NewVersion(num); err != nil {
		return "", errors.WrapWithDetails(ErrNoRelease, "build stamp %q is not a version", "stamp", stamp)
	}
	return num, nil
}

// ArchiveName is the goreleaser artifact name for a target, e.g.
// human_0.21.0_linux_arm64.tar.gz.
func ArchiveName(version, goos, goarch string) string {
	return fmt.Sprintf("human_%s_%s_%s.tar.gz", version, goos, goarch)
}

// FetchBinary downloads the release archive for version/goos/goarch, verifies
// its sha256 against that release's checksums.txt, extracts the human entry and
// publishes it at destPath by rename — so a concurrent reader either sees the
// previous file or the complete new one, never a partial download. progress may
// be nil.
func FetchBinary(ctx context.Context, version, goos, goarch, destPath string, progress io.Writer) error {
	num, err := Version(version)
	if err != nil {
		return err
	}
	if goos == "windows" {
		// The windows artifact is a .zip; adding an untested branch for a
		// target no caller has today would be code nothing exercises.
		return errors.WithDetails("release archive for windows is a zip and is not supported yet",
			"goos", goos, "version", num)
	}

	name := ArchiveName(num, goos, goarch)
	tagURL := baseURL + "/v" + num

	want, err := checksumFor(ctx, tagURL+"/"+checksumsName, name)
	if err != nil {
		return err
	}

	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating release cache directory", "path", destDir)
	}

	if progress != nil {
		_, _ = fmt.Fprintf(progress, "Downloading human %s for %s/%s...\n", num, goos, goarch)
	}

	archivePath, err := downloadAndVerify(ctx, tagURL+"/"+name, name, want, destDir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archivePath) }()

	return extractEntry(archivePath, name, destPath)
}

// checksumFor downloads the release's checksums.txt and returns the recorded
// sha256 for archive. The file's format is "<sha256><spaces><name>", one entry
// per artifact.
func checksumFor(ctx context.Context, url, archive string) (string, error) {
	resp, err := httpGet(ctx, url)
	if err != nil {
		return "", errors.WrapWithDetails(err, "downloading release checksums", "url", url)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.WithDetails("release checksums request failed with HTTP %d", "status", resp.StatusCode, "url", url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumBytes))
	if err != nil {
		return "", errors.WrapWithDetails(err, "reading release checksums", "url", url)
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[len(fields)-1] == archive {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", errors.WithDetails("release publishes no such archive", "archive", archive, "url", url)
}

// downloadAndVerify streams the archive to a temp file in dir while hashing it,
// and keeps the file only when the hash matches want. A mismatch leaves nothing
// on disk: an unverified archive is never a cache entry, not even briefly.
func downloadAndVerify(ctx context.Context, url, archive, want, dir string) (string, error) {
	resp, err := httpGet(ctx, url)
	if err != nil {
		return "", errors.WrapWithDetails(err, "downloading release archive", "url", url)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.WithDetails("release archive request failed with HTTP %d", "status", resp.StatusCode, "url", url)
	}

	tmp, err := os.CreateTemp(dir, ".human-dl-*.tar.gz")
	if err != nil {
		return "", errors.WrapWithDetails(err, "creating release download file", "dir", dir)
	}
	// The temp file survives only a verified download: one cleanup for every
	// failure below, so a partial or forged archive is never left where a later
	// launch could pick it up.
	tmpName := tmp.Name()
	verified := false
	defer func() {
		if !verified {
			// #nosec G703 -- tmpName is the file os.CreateTemp just made in dir; no input reaches it
			_ = os.Remove(tmpName)
		}
	}()

	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, maxArchiveBytes+1))
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return "", errors.WrapWithDetails(copyErr, "writing release archive", "url", url)
	case closeErr != nil:
		return "", errors.WrapWithDetails(closeErr, "writing release archive", "url", url)
	case n > maxArchiveBytes:
		return "", errors.WithDetails("release archive exceeds size limit", "archive", archive, "limit", maxArchiveBytes)
	}

	if got := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, want) {
		return "", errors.WithDetails("release archive checksum mismatch",
			"archive", archive, "expected", want, "actual", got)
	}
	verified = true
	return tmpName, nil
}

// extractEntry pulls the human binary out of the verified archive and publishes
// it at destPath by rename, so no reader ever sees a half-written executable.
func extractEntry(archivePath, archive, destPath string) error {
	f, err := os.Open(archivePath) // #nosec G304 -- a temp file this package just created
	if err != nil {
		return errors.WrapWithDetails(err, "opening release archive", "path", archivePath)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.WrapWithDetails(err, "decompressing release archive", "archive", archive)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.WrapWithDetails(err, "reading release archive", "archive", archive)
		}
		// Compare the base name so an archive that later gains a directory
		// prefix still resolves to the same entry.
		if header.Typeflag != tar.TypeReg || path.Base(header.Name) != binaryEntry {
			continue
		}
		return publish(tr, destPath, archive)
	}
	return errors.WithDetails("release archive contains no human binary", "archive", archive)
}

// publish writes the entry to a temp file beside destPath and renames it into
// place, executable. Rename is atomic within the directory, which is what makes
// two launches racing for the same version safe.
func publish(r io.Reader, destPath, archive string) error {
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".human-bin-*")
	if err != nil {
		return errors.WrapWithDetails(err, "creating release binary file", "dir", dir)
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		if !published {
			// #nosec G703 -- tmpName is the file os.CreateTemp just made in dir; no input reaches it
			_ = os.Remove(tmpName)
		}
	}()

	n, copyErr := io.Copy(tmp, io.LimitReader(r, maxBinaryBytes+1))
	closeErr := tmp.Close()
	switch {
	case copyErr != nil:
		return errors.WrapWithDetails(copyErr, "writing release binary", "path", destPath)
	case closeErr != nil:
		return errors.WrapWithDetails(closeErr, "writing release binary", "path", destPath)
	case n > maxBinaryBytes:
		return errors.WithDetails("release binary exceeds size limit", "archive", archive, "limit", maxBinaryBytes)
	}

	// #nosec G302,G703 -- the container executes this file, and tmpName is the temp file created above
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return errors.WrapWithDetails(err, "making release binary executable", "path", destPath)
	}
	// #nosec G703 -- both paths are composed by this package from the caller's destination
	if err := os.Rename(tmpName, destPath); err != nil {
		return errors.WrapWithDetails(err, "publishing release binary", "path", destPath)
	}
	published = true
	return nil
}
