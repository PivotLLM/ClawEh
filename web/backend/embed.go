// Package webserver hosts the embedded WebUI frontend assets and the
// route-registration entry point that the merged binary uses to mount the
// WebUI on the gateway's shared HTTP mux.
package webserver

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/app"
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
	mux.Handle("/", spaHandler(subFS))
}

// Placeholders the frontend's index.html and site.webmanifest carry where the
// product's names go. They are filled in when the file is served, so the page
// has the names from its first byte and the bundle holds none (see
// web/frontend/src/store/product.ts).
const (
	placeholderName       = "{{PRODUCT_NAME}}"
	placeholderShortName  = "{{PRODUCT_SHORT_NAME}}"
	placeholderBinaryName = "{{PRODUCT_BINARY_NAME}}"
)

// templatedFile is an embedded file served with the product names filled in,
// each value escaped for the file's syntax.
type templatedFile struct {
	contentType string
	escape      func(string) string
}

var templatedFiles = map[string]templatedFile{
	"index.html":       {"text/html; charset=utf-8", html.EscapeString},
	"site.webmanifest": {"application/manifest+json", jsonStringContent},
}

// jsonStringContent is s escaped for use between the quotes of a JSON string:
// the quote, the backslash and control characters (RFC 8259 §7).
func jsonStringContent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fillNames replaces the name placeholders in data with the product's names,
// escaped by escape.
func fillNames(data []byte, escape func(string) string) []byte {
	return []byte(strings.NewReplacer(
		placeholderName, escape(app.Name()),
		placeholderShortName, escape(app.ShortName()),
		placeholderBinaryName, escape(app.BinaryName()),
	).Replace(string(data)))
}

// renderTemplated reads each templated file in fsys and fills in the names.
// The names do not change while the process runs, so this is done once. A file
// that is missing (a build without the frontend) is left out.
func renderTemplated(fsys fs.FS) map[string][]byte {
	out := map[string][]byte{}
	for name, tf := range templatedFiles {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		out[name] = fillNames(data, tf.escape)
	}
	return out
}

// serveRendered writes a templated file. It is revalidated on every load like
// any unhashed file (cacheControl), so a renamed or upgraded binary is seen at
// once.
func serveRendered(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Content-Type", templatedFiles[name].contentType)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// spaHandler serves the frontend in fsys: files as they are, the templated
// ones with the names filled in, and index.html for any other path so the
// client-side router can take it.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	rendered := renderTemplated(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		if cleanPath != "" && cleanPath != "index.html" {
			if data, ok := rendered[cleanPath]; ok {
				serveRendered(w, r, cleanPath, data)
				return
			}
			if _, statErr := fs.Stat(fsys, cleanPath); statErr == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
			// Missing asset-like paths should remain 404.
			if strings.Contains(path.Base(cleanPath), ".") {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		if data, ok := rendered["index.html"]; ok {
			serveRendered(w, r, "index.html", data)
			return
		}
		indexReq := r.Clone(r.Context())
		indexReq.URL.Path = "/"
		fileServer.ServeHTTP(w, indexReq)
	})
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
