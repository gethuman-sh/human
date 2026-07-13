package starter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Typeflag: typeflag,
			Linkname: e.linkname,
		}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
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

// stubDownload replaces httpGet for the test, serving the given body with the
// given status.
func stubDownload(t *testing.T, status int, body []byte) {
	t.Helper()
	orig := httpGet
	httpGet = func(_ context.Context, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	}
	t.Cleanup(func() { httpGet = orig })
}

func webGoTemplate() Template {
	tpl, ok := Lookup("web", "go")
	if !ok {
		panic("web/go template missing")
	}
	return tpl
}

func TestFetchExtractsOnlyTemplateSubdir(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/", typeflag: tar.TypeDir},
		{name: "starters-main/README.md", body: "layout contract"},
		{name: "starters-main/web/", typeflag: tar.TypeDir},
		{name: "starters-main/web/go/", typeflag: tar.TypeDir},
		{name: "starters-main/web/go/go.mod", body: "module webapp"},
		{name: "starters-main/web/go/main.go", body: "package main"},
		{name: "starters-main/web/go/static/", typeflag: tar.TypeDir},
		{name: "starters-main/web/go/static/index.html", body: "<html>"},
		{name: "starters-main/web/rust/main.rs", body: "fn main() {}"},
	})
	stubDownload(t, http.StatusOK, archive)
	dir := t.TempDir()

	res, err := Fetch(context.Background(), webGoTemplate(), dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Created != 3 || res.Skipped != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || string(got) != "module webapp" {
		t.Fatalf("go.mod = %q, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "static", "index.html")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
	// Sibling templates and repo-root files must not leak into the project.
	for _, absent := range []string{"main.rs", "README.md", "web"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); !os.IsNotExist(err) {
			t.Fatalf("unexpected entry %q extracted", absent)
		}
	}
}

func TestFetchPreservesExecBit(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/web/go/run.sh", body: "#!/bin/sh", mode: 0o755},
		{name: "starters-main/web/go/main.go", body: "package main"},
	})
	stubDownload(t, http.StatusOK, archive)
	dir := t.TempDir()

	if _, err := Fetch(context.Background(), webGoTemplate(), dir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v", info.Mode())
	}
	plain, err := os.Stat(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Mode().Perm() != 0o600 {
		t.Fatalf("regular file mode = %v, want 0600", plain.Mode().Perm())
	}
}

func TestFetchSkipsExistingFiles(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/web/go/README.md", body: "template readme"},
		{name: "starters-main/web/go/main.go", body: "package main"},
	})
	stubDownload(t, http.StatusOK, archive)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Fetch(context.Background(), webGoTemplate(), dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Created != 1 || res.Skipped != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if string(got) != "mine" {
		t.Fatalf("existing file overwritten: %q", got)
	}
}

func TestFetchRejectsTraversalEntries(t *testing.T) {
	cases := []string{
		"starters-main/web/go/../../../evil.go",
		"starters-main/web/go/../../evil.go",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			archive := makeTarGz(t, []tarEntry{{name: name, body: "evil"}})
			stubDownload(t, http.StatusOK, archive)
			dir := t.TempDir()

			if _, err := Fetch(context.Background(), webGoTemplate(), dir); err == nil {
				t.Fatal("expected traversal entry to be rejected")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("files written despite rejection: %v", entries)
			}
		})
	}
}

func TestFetchIgnoresSymlinks(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/web/go/main.go", body: "package main"},
		{name: "starters-main/web/go/evil", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	stubDownload(t, http.StatusOK, archive)
	dir := t.TempDir()

	res, err := Fetch(context.Background(), webGoTemplate(), dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Fatal("symlink entry materialized")
	}
}

func TestFetchTemplateMissingFromArchive(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/web/rust/main.rs", body: "fn main() {}"},
	})
	stubDownload(t, http.StatusOK, archive)

	_, err := Fetch(context.Background(), webGoTemplate(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "template not found") {
		t.Fatalf("expected template-not-found error, got %v", err)
	}
}

func TestFetchHTTPError(t *testing.T) {
	stubDownload(t, http.StatusNotFound, nil)
	if _, err := Fetch(context.Background(), webGoTemplate(), t.TempDir()); err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestFetchCorruptGzip(t *testing.T) {
	stubDownload(t, http.StatusOK, []byte("not a gzip stream"))
	if _, err := Fetch(context.Background(), webGoTemplate(), t.TempDir()); err == nil {
		t.Fatal("expected error for corrupt archive")
	}
}

func TestFetchEntrySizeCap(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "starters-main/web/go/huge.bin", body: strings.Repeat("a", maxEntryBytes+2)},
	})
	stubDownload(t, http.StatusOK, archive)

	_, err := Fetch(context.Background(), webGoTemplate(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestFetchDownloadFailure(t *testing.T) {
	orig := httpGet
	httpGet = func(_ context.Context, _ string) (*http.Response, error) {
		return nil, context.Canceled
	}
	t.Cleanup(func() { httpGet = orig })

	if _, err := Fetch(context.Background(), webGoTemplate(), t.TempDir()); err == nil {
		t.Fatal("expected error when download fails")
	}
}
