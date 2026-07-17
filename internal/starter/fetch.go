package starter

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	humanerrors "github.com/gethuman-sh/human/errors"
)

const (
	// tarballURL and tarballRoot are pinned together: codeload prefixes every
	// archive entry with <repo>-<ref>/, so renaming the repo or its default
	// branch must update both.
	tarballURL  = "https://codeload.github.com/gethuman-sh/starters/tar.gz/refs/heads/main"
	tarballRoot = "starters-main/"

	// Decompression caps: a starter is a handful of small files, so anything
	// near these limits is a corrupt or hostile archive, not a template
	// (gosec G110 decompression-bomb guard).
	maxEntryBytes = 10 << 20
	maxTotalBytes = 100 << 20
)

// fetchHTTPClient bounds the download so a black-holed connection cannot hang
// the wizard's "creating" state forever.
var fetchHTTPClient = &http.Client{Timeout: 60 * time.Second}

// httpGet is the HTTP entry point — replaced with a stub in tests so no test
// ever touches the network.
var httpGet = func(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return fetchHTTPClient.Do(req)
}

// FetchResult reports what scaffolding changed so the UI can say
// "N files created" and surface untouched pre-existing files.
type FetchResult struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
}

// Fetch downloads the starters tarball and extracts only tpl.Path into
// destDir, stripping the repo/template prefix. Existing files are skipped,
// never overwritten: the wizard runs in directories that may already hold a
// README or .gitignore the user wants to keep.
func Fetch(ctx context.Context, tpl Template, destDir string) (FetchResult, error) {
	resp, err := httpGet(ctx, tarballURL)
	if err != nil {
		return FetchResult{}, humanerrors.WrapWithDetails(err, "downloading starter archive", "url", tarballURL)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return FetchResult{}, humanerrors.WithDetails("starter archive request failed", "url", tarballURL, "status", resp.StatusCode)
	}
	prefix := tarballRoot + tpl.Path + "/"
	res, err := extractTemplate(resp.Body, prefix, destDir)
	if err != nil {
		return FetchResult{}, err
	}
	if res.Created == 0 && res.Skipped == 0 {
		// Zero matching entries means the repo layout drifted (template moved,
		// branch renamed) — fail loudly instead of reporting an empty success.
		return FetchResult{}, humanerrors.WithDetails("template not found in starter archive", "path", tpl.Path)
	}
	return res, nil
}

// extractTemplate streams the gzipped tarball, writing only entries under
// prefix into destDir.
func extractTemplate(r io.Reader, prefix, destDir string) (FetchResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return FetchResult{}, humanerrors.WrapWithDetails(err, "decompressing starter archive")
	}
	defer func() { _ = gz.Close() }()

	var res FetchResult
	var total int64
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return res, nil
		}
		if err != nil {
			return FetchResult{}, humanerrors.WrapWithDetails(err, "reading starter archive")
		}
		if !strings.HasPrefix(header.Name, prefix) {
			continue
		}
		dest, ok := safeDestPath(destDir, header.Name, prefix)
		if !ok {
			return FetchResult{}, humanerrors.WithDetails("unsafe path in starter archive", "entry", header.Name)
		}
		if dest == "" {
			// The template root directory itself — destDir already exists.
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o750); err != nil {
				return FetchResult{}, humanerrors.WrapWithDetails(err, "creating starter directory", "path", dest)
			}
		case tar.TypeReg:
			created, err := writeEntry(tr, header, dest, &total)
			if err != nil {
				return FetchResult{}, err
			}
			if created {
				res.Created++
			} else {
				res.Skipped++
			}
		default:
			// Symlinks, hardlinks and device nodes have no legitimate place in
			// a starter template — drop them rather than materialize anything
			// that could point outside destDir.
		}
	}
}

// writeEntry writes one regular file, skipping pre-existing files and
// enforcing the decompression caps. Returns whether the file was created.
func writeEntry(tr io.Reader, header *tar.Header, dest string, total *int64) (bool, error) {
	if _, err := os.Lstat(dest); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return false, humanerrors.WrapWithDetails(err, "creating starter directory", "path", filepath.Dir(dest))
	}
	// Preserve only the exec bit: templates may ship scripts, but archive
	// modes are otherwise untrusted input.
	mode := os.FileMode(0o600)
	if header.FileInfo().Mode()&0o100 != 0 {
		mode = 0o700
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- dest is validated by safeDestPath to stay inside destDir
	if err != nil {
		return false, humanerrors.WrapWithDetails(err, "creating starter file", "path", dest)
	}
	n, err := io.Copy(f, io.LimitReader(tr, maxEntryBytes+1))
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(dest)
		return false, humanerrors.WrapWithDetails(err, "writing starter file", "path", dest)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return false, humanerrors.WrapWithDetails(closeErr, "writing starter file", "path", dest)
	}
	// A rejected entry must not stay on disk: the outer loop opens with
	// O_EXCL, so a leftover would jam every subsequent Fetch on the same path
	// until the user cleaned it up manually.
	if n > maxEntryBytes {
		_ = os.Remove(dest)
		return false, humanerrors.WithDetails("starter file exceeds size limit", "entry", header.Name, "limit", maxEntryBytes)
	}
	*total += n
	if *total > maxTotalBytes {
		_ = os.Remove(dest)
		return false, humanerrors.WithDetails("starter archive exceeds size limit", "limit", maxTotalBytes)
	}
	return true, nil
}

// safeDestPath maps an archive entry name to its destination path, rejecting
// anything that would escape destDir (path traversal, absolute paths). The
// empty-string/true return marks the template root entry itself.
func safeDestPath(destDir, name, prefix string) (string, bool) {
	rel := path.Clean(strings.TrimPrefix(name, prefix))
	if rel == "." || rel == "" {
		return "", true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	dest := filepath.Join(destDir, filepath.FromSlash(rel))
	// Belt and suspenders: the Clean checks above should already guarantee
	// containment, but verify against the final joined path.
	root := filepath.Clean(destDir)
	if dest != root && !strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return "", false
	}
	return dest, true
}
