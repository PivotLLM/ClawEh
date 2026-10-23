package webserver

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// manualFS holds the user manual: the VitePress build of manual/ (make
// manual), written into web/backend/manual. The build uses base /manual/ and
// plain .html file names, so files are served as they are.
//
//go:embed all:manual
var manualFS embed.FS

// manualPrefix is where the manual is served. It sits behind the WebUI login
// (middleware.authProtected).
const manualPrefix = "/manual/"

// RegisterManualRoutes serves the embedded manual under /manual/. /manual
// redirects to /manual/; a path the build does not contain is a 404 with the
// manual's own not-found page.
func RegisterManualRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(manualFS, "manual")
	if err != nil {
		logger.WarnCF("web", "Embedded manual unavailable", map[string]any{"error": err.Error()})
		return
	}
	mux.Handle("/manual", http.RedirectHandler(manualPrefix, http.StatusMovedPermanently))
	mux.Handle(manualPrefix, manualHandler(sub))
}

// manualHandler serves files from the manual build in fsys. Directories are
// served only through their index.html, never as a listing.
func manualHandler(fsys fs.FS) http.Handler {
	fileServer := http.StripPrefix(strings.TrimSuffix(manualPrefix, "/"), http.FileServer(http.FS(fsys)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := path.Clean(strings.TrimPrefix(r.URL.Path, manualPrefix))
		if name == "." {
			name = ""
		}
		target := name
		if info, err := fs.Stat(fsys, orDot(name)); err == nil && info.IsDir() {
			target = path.Join(name, "index.html")
		}
		if info, err := fs.Stat(fsys, orDot(target)); err != nil || info.IsDir() || strings.HasPrefix(path.Base(target), ".") {
			serveManualNotFound(w, fsys)
			return
		}
		w.Header().Set("Cache-Control", cacheControl(name))
		fileServer.ServeHTTP(w, r)
	})
}

// orDot maps the manual root ("") to the fs.FS root name.
func orDot(name string) string {
	if name == "" {
		return "."
	}
	return name
}

// serveManualNotFound answers 404 with the manual's own not-found page, or a
// plain 404 when the build has none.
func serveManualNotFound(w http.ResponseWriter, fsys fs.FS) {
	body, err := fs.ReadFile(fsys, "404.html")
	if err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
	if _, err := w.Write(body); err != nil {
		logger.DebugCF("http", "response write failed", map[string]any{"error": err.Error()})
	}
}
