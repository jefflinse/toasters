package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

// requestIDKey is the context key for the request ID.
const requestIDKey contextKey = "request_id"

// requestIDFromContext returns the request ID from the context, or empty string.
func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// recoveryMiddleware catches panics, logs the stack trace, and returns 500.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				slog.Error("panic recovered in HTTP handler",
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(stack),
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", requestIDFromContext(r.Context()),
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestIDMiddleware generates a UUID v4 X-Request-ID header and propagates
// it to the request context. Client-supplied IDs are validated: only
// alphanumeric, hyphens, underscores, and dots are allowed, max 64 chars.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !isValidRequestID(id) {
			u, err := uuid.NewV4()
			if err == nil {
				id = u.String()
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isValidRequestID checks that a request ID is non-empty, at most 64 chars,
// and contains only safe characters (alphanumeric, hyphens, underscores, dots).
func isValidRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the underlying ResponseWriter, enabling http.Flusher
// detection through the wrapper.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// loggingMiddleware logs method, path, status code, duration, and request ID.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode,
			"duration_ms", duration.Milliseconds(),
			"request_id", requestIDFromContext(r.Context()),
		)
	})
}

// corsMiddleware allows * origin with standard headers and methods.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Location")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// contentTypeMiddleware validates Content-Type: application/json on requests
// with bodies (POST, PUT, PATCH). Skips GET, DELETE, OPTIONS, and requests
// with no body.
func contentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			// Only validate if there appears to be a body. Skip validation
			// for no-body requests (e.g. POST /cancel, POST /promote).
			if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			ct := r.Header.Get("Content-Type")
			if ct == "" || !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusBadRequest, "bad_request",
					"Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// chain applies middleware in order (outermost first).
// chain(a, b, c)(handler) produces a(b(c(handler))).
func chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
