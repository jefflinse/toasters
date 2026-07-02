package provider

import (
	"errors"
	"fmt"
	"strings"
)

// APIError is a non-200 response from an LLM endpoint, preserving the status
// code and a bounded copy of the body so callers can classify failures
// (rate limit vs context overflow vs auth) instead of string-matching
// free-form messages.
type APIError struct {
	Provider   string // provider display name
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API error (%d): %s", e.Provider, e.StatusCode, e.Body)
}

// overflowMarkers are lowercase substrings that identify a context-window
// overflow across providers. Best-effort by nature: OpenAI-compatible
// servers use the "context_length_exceeded" code, Anthropic says "prompt is
// too long", llama.cpp and LM Studio use assorted phrasings around "context".
var overflowMarkers = []string{
	"context_length_exceeded",
	"prompt is too long",
	"maximum context length",
	"exceeds the available context",
	"context length",
	"context window",
	"too many tokens",
}

// IsContextOverflow reports whether err looks like a context-window overflow
// from an LLM endpoint. It prefers the typed APIError (status 4xx + body
// match) and falls back to matching the error string, since stream errors
// arrive as wrapped fmt.Errorf chains.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// Overflows are client errors; never classify 5xx/429 as overflow
		// even if the body mentions context.
		if apiErr.StatusCode < 400 || apiErr.StatusCode >= 500 || apiErr.StatusCode == 429 {
			return false
		}
		return containsOverflowMarker(apiErr.Body)
	}
	return containsOverflowMarker(err.Error())
}

func containsOverflowMarker(s string) bool {
	s = strings.ToLower(s)
	for _, marker := range overflowMarkers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
