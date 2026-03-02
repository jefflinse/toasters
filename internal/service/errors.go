package service

import (
	"regexp"
)

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
