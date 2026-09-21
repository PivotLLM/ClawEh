// Package webserver hosts the embedded WebUI frontend assets and the
// route-registration entry point that the merged claw binary uses to mount
// the WebUI on the gateway's shared HTTP mux.
package webserver

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

//go:embed all:dist
var frontendFS embed.FS

// RegisterEmbedRoutes sets up the HTTP handler to serve the embedded frontend
// assets on the given mux. Unknown /api/* paths remain 404 (so the gateway's
// own /api/* handlers are reachable without being shadowed by the SPA fallback).
func RegisterEmbedRoutes(mux *http.ServeMux) {
	// Go's built-in mime.TypeByExtension returns "image/svg" which is incorrect.
	// The correct MIME type per RFC 6838 is "image/svg+xml".
	if err := mime.AddExtensionType(".svg", "image/svg+xml"); err != nil {
		logger.WarnCF("web", "Failed to register SVG MIME type", map[string]any{"error": err.Error()})
	}

	subFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		logger.WarnC("web", "No 'dist' folder found in embedded frontend — run `pnpm build:backend` in web/frontend before building")
		return
	}

	fileServer := http.FileServer(http.FS(subFS))

	mux.Handle(
		"/",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.NotFound(w, r)
				return
			}

			// Keep unknown API paths as 404 instead of falling back to SPA entry.
			if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}

			cleanPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
			if cleanPath == "." {
				cleanPath = ""
			}

			w.Header().Set("Cache-Control", cacheControl(cleanPath))

			if cleanPath != "" {
				if _, statErr := fs.Stat(subFS, cleanPath); statErr == nil {
					fileServer.ServeHTTP(w, r)
					return
				}
				// Missing asset-like paths should remain 404.
				if strings.Contains(path.Base(cleanPath), ".") {
					fileServer.ServeHTTP(w, r)
					return
				}
			}

			indexReq := r.Clone(r.Context())
			indexReq.URL.Path = "/"
			fileServer.ServeHTTP(w, indexReq)
		}),
	)
}

// cacheControl picks the Cache-Control value for an embedded file. Embedded
// files carry no modification time, so without an explicit header a browser
// has nothing to revalidate against and can keep an old index.html (and the
// old chunks it names) after a deploy. The SPA entry and other unhashed files
// must always be revalidated; the Vite output under assets/ is content-hashed,
// so it can be cached indefinitely.
func cacheControl(cleanPath string) string {
	if strings.HasPrefix(cleanPath, "assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}
