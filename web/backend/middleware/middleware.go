package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// JSONContentType sets the Content-Type header to application/json for
// API requests handled by the wrapped handler.
// SSE endpoints (text/event-stream) are excluded.
func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}

// responseRecorder wraps http.ResponseWriter to capture the status code.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter if it implements http.Flusher.
// This is required for SSE (Server-Sent Events) to work through the middleware.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so that http.ResponseController
// and interface checks (like http.Flusher) can see through the wrapper.
func (rr *responseRecorder) Unwrap() http.ResponseWriter {
	return rr.ResponseWriter
}

// Logger logs each HTTP request with method, path, status code, and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.InfoCF("http", fmt.Sprintf("%s %s", r.Method, r.URL.Path), map[string]any{
			"status":   rec.statusCode,
			"duration": time.Since(start).String(),
		})
	})
}

// Recoverer recovers from panics in downstream handlers and returns a 500
// Internal Server Error response.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.ErrorCF("http", "Panic recovered", map[string]any{
					"error": fmt.Sprintf("%v", err),
					"stack": string(debug.Stack()),
				})
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
