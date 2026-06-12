package gui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// distFS embeds the built frontend. The repo only commits a .gitkeep in
// dist/ (the all: prefix picks up dotfiles, which keeps the embed valid);
// `make web` writes the real Vite build there before release builds.
// Without built assets, staticHandler serves placeholderHTML instead so
// `go build` works for contributors without node.
//
//go:embed all:dist
var distFS embed.FS

// placeholderHTML is served when the binary was built without frontend
// assets. Styled with the brand palette so even the fallback looks owned.
const placeholderHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>human gui</title>
    <style>
      body { background: #14141f; color: #fac86a; font-family: ui-monospace, monospace;
             display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
      div { text-align: center; }
      code { color: #4ee8c4; }
    </style>
  </head>
  <body>
    <div>
      <h1>human gui</h1>
      <p>The GUI assets are not built into this binary.</p>
      <p>Run <code>make web</code> and rebuild, or use a release binary.</p>
    </div>
  </body>
</html>
`

// staticHandler serves the embedded frontend with an SPA fallback: any
// path that is not a real asset returns index.html so client-side routes
// survive a browser refresh.
func staticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable with the committed dist/.gitkeep; guards against the
		// embed directive and directory drifting apart.
		return placeholderOnly()
	}
	if _, statErr := fs.Stat(sub, "index.html"); statErr != nil {
		return placeholderOnly()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, openErr := sub.Open(path); openErr == nil {
				_ = f.Close()
				// Vite emits content-hashed filenames under assets/, safe
				// to cache hard; everything else stays revalidated.
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

func placeholderOnly() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
	})
}
