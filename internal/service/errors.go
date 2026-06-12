package service

import (
	"fmt"
	"regexp"
)

// ---------------------------------------------------------------------------
// Sentinel errors
//
// Service methods wrap these so transports (the HTTP status mapping in
// internal/server) and clients can classify failures with errors.Is instead
// of string matching.
// ---------------------------------------------------------------------------

// ErrNotFound is returned by Get methods when the requested entity does not
// exist. Callers can check with errors.Is(err, service.ErrNotFound).
//
// Note: this is defined as a variable (not a type) so it can be used with
// errors.Is across package boundaries, including when wrapped with fmt.Errorf.
var ErrNotFound = errNotFound("not found")

type errNotFound string

func (e errNotFound) Error() string { return string(e) }

var (
	// ErrConflict marks a request that is valid but clashes with current
	// state: a duplicate name, a turn already in progress, a job past the
	// point of cancellation. Maps to HTTP 409.
	ErrConflict = sentinel("conflict")

	// ErrUnavailable marks a request that can't be served because a required
	// subsystem (operator, store, runtime, provider) isn't configured or
	// reachable. Maps to HTTP 503.
	ErrUnavailable = sentinel("unavailable")

	// ErrInvalid marks input that is well-formed at the transport layer but
	// rejected by domain rules (bad provider ID, protected definition).
	// Maps to HTTP 422.
	ErrInvalid = sentinel("invalid")

	// ErrBusy marks a request rejected because too many operations are in
	// flight. Maps to HTTP 429.
	ErrBusy = sentinel("busy")
)

type sentinel string

func (s sentinel) Error() string { return string(s) }

// classified couples a human-readable message with a sentinel class so the
// message stays clean ("operator turn already in progress", no "conflict:"
// prefix) while errors.Is(err, ErrConflict) still matches.
type classified struct {
	class sentinel
	msg   string
}

func (c *classified) Error() string { return c.msg }
func (c *classified) Unwrap() error { return c.class }

// Conflictf formats an error that satisfies errors.Is(err, ErrConflict).
func Conflictf(format string, args ...any) error {
	return &classified{class: ErrConflict, msg: fmt.Sprintf(format, args...)}
}

// Unavailablef formats an error that satisfies errors.Is(err, ErrUnavailable).
func Unavailablef(format string, args ...any) error {
	return &classified{class: ErrUnavailable, msg: fmt.Sprintf(format, args...)}
}

// Invalidf formats an error that satisfies errors.Is(err, ErrInvalid).
func Invalidf(format string, args ...any) error {
	return &classified{class: ErrInvalid, msg: fmt.Sprintf(format, args...)}
}

// Busyf formats an error that satisfies errors.Is(err, ErrBusy).
func Busyf(format string, args ...any) error {
	return &classified{class: ErrBusy, msg: fmt.Sprintf(format, args...)}
}

// sanitizedError wraps an original error with a cleaned message that has
// filesystem paths replaced. It preserves the error chain so that
// errors.Is and errors.As continue to work against the original error.
type sanitizedError struct {
	msg      string
	original error
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.original }

// pathPattern matches absolute filesystem paths. It captures sequences starting
// with / followed by path-like characters (letters, digits, dots, hyphens,
// underscores, and path separators). This intentionally covers /Users/...,
// /home/..., /tmp/..., and other absolute paths.
var pathPattern = regexp.MustCompile(`/(?:Users|home|tmp|var|etc|opt|usr|private)[/][^\s"':;,\)]+`)

// sanitizeErrorString returns the sanitized error message string.
// Convenience wrapper for use in OperationFailedPayload.Error fields.
func sanitizeErrorString(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeError(err).Error()
}

// SanitizeErrorMessage replaces filesystem paths in a string with "[path]".
// Used by the HTTP server layer to sanitize error messages before sending
// them to clients.
func SanitizeErrorMessage(s string) string {
	return pathPattern.ReplaceAllString(s, "[path]")
}

// sanitizeError replaces filesystem paths in the error message with "[path]".
// It returns nil for nil errors. The returned error preserves the original
// error chain for errors.Is/errors.As via Unwrap().
func sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	cleaned := pathPattern.ReplaceAllString(err.Error(), "[path]")
	if cleaned == err.Error() {
		return err // no paths found, return original to preserve exact type
	}
	return &sanitizedError{
		msg:      cleaned,
		original: err,
	}
}
