package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jefflinse/toasters/internal/service"
)

// writeJSON marshals v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Best-effort: headers already sent, nothing we can do.
		_ = err
	}
}

// writeError writes a standard error envelope to w. The caller is responsible
// for sanitizing the message (e.g. via sanitizePath) before passing it here.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// decodeBody decodes the JSON request body into v. Returns false and writes
// an error response if decoding fails.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "request body is required")
		return false
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("invalid JSON: %s", err.Error()))
		return false
	}
	return true
}

// parsePagination extracts limit and offset from query parameters with
// validation and defaults.
func parsePagination(r *http.Request) (PaginationParams, error) {
	p := PaginationParams{Limit: 50, Offset: 0}

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("invalid limit: %s", v)
		}
		if n < 0 || n > 200 {
			return p, fmt.Errorf("limit must be between 0 and 200, got %d", n)
		}
		p.Limit = n
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("invalid offset: %s", v)
		}
		if n < 0 {
			return p, fmt.Errorf("offset must be non-negative, got %d", n)
		}
		p.Offset = n
	}

	return p, nil
}

// paginate applies limit/offset to a slice and returns the paginated items
// along with the total count.
func paginate[T any](items []T, p PaginationParams) ([]T, int) {
	total := len(items)
	if p.Offset >= total {
		return []T{}, total
	}
	end := p.Offset + p.Limit
	if end > total {
		end = total
	}
	return items[p.Offset:end], total
}

// mapServiceError maps a service-layer error to an HTTP status code and error code.
func mapServiceError(err error) (status int, code string) {
	if err == nil {
		return http.StatusOK, ""
	}

	msg := err.Error()

	// Check for ErrNotFound in the error chain.
	if errors.Is(err, service.ErrNotFound) {
		return http.StatusNotFound, "not_found"
	}

	// Check for specific error patterns from the service layer.
	switch {
	case strings.Contains(msg, "too many concurrent operations"):
		return http.StatusTooManyRequests, "too_many_requests"
	case strings.Contains(msg, "already exists"):
		return http.StatusConflict, "conflict"
	case strings.Contains(msg, "turn already in progress"):
		return http.StatusConflict, "conflict"
	case strings.Contains(msg, "cannot be cancelled"):
		return http.StatusConflict, "conflict"
	case strings.Contains(msg, "is already complete"):
		return http.StatusConflict, "conflict"
	case strings.Contains(msg, "cannot delete system"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot delete read-only"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot delete agent"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot add skill to system"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot add agent to read-only"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot set coordinator on read-only"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "is not an auto-team"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot promote system"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "cannot detect coordinator for read-only"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "source file unknown"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "has no source path"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "outside user directory"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "outside config directory"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "outside the teams directory"):
		return http.StatusUnprocessableEntity, "unprocessable_entity"
	case strings.Contains(msg, "operator not configured"):
		return http.StatusServiceUnavailable, "service_unavailable"
	case strings.Contains(msg, "LLM provider not configured"):
		return http.StatusServiceUnavailable, "service_unavailable"
	case strings.Contains(msg, "store not configured"):
		return http.StatusServiceUnavailable, "service_unavailable"
	case strings.Contains(msg, "runtime not configured"):
		return http.StatusServiceUnavailable, "service_unavailable"
	case strings.Contains(msg, "provider unreachable"):
		return http.StatusServiceUnavailable, "service_unavailable"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// pathPattern matches absolute filesystem paths that should be sanitized.
var pathPattern = regexp.MustCompile(`/(?:Users|home|tmp|var|etc|opt|usr|private)[/][^\s"':;,)]+`)

// sanitizePath replaces filesystem paths in a string with "[path]".
func sanitizePath(s string) string {
	return pathPattern.ReplaceAllString(s, "[path]")
}
