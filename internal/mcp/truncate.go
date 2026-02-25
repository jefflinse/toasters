package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// TruncatingCaller wraps an MCPCaller and truncates results that exceed maxLen.
// This is useful for wrapping a Manager before passing it to packages that
// cannot import internal/mcp directly (e.g. internal/runtime).
type TruncatingCaller struct {
	inner  MCPCaller
	maxLen int
}

// NewTruncatingCaller creates a TruncatingCaller that wraps the given caller.
// If maxLen <= 0, DefaultMaxResultLen is used.
func NewTruncatingCaller(inner MCPCaller, maxLen int) *TruncatingCaller {
	if maxLen <= 0 {
		maxLen = DefaultMaxResultLen
	}
	return &TruncatingCaller{inner: inner, maxLen: maxLen}
}

// Call dispatches the tool call and truncates the result if it exceeds maxLen.
func (tc *TruncatingCaller) Call(ctx context.Context, namespacedName string, args json.RawMessage) (string, error) {
	result, err := tc.inner.Call(ctx, namespacedName, args)
	if err != nil {
		return "", err
	}
	return TruncateResult(result, tc.maxLen), nil
}

// DefaultMaxResultLen is the default maximum byte length for MCP tool results.
// At ~4 chars/token, 16000 bytes ≈ 4000 tokens — a reasonable budget for a single tool result.
const DefaultMaxResultLen = 16000

// TruncateResult truncates an MCP tool result to fit within maxLen bytes.
// It first applies JSON slimming to remove low-value fields, then attempts
// JSON-aware truncation (shrinking arrays), and finally falls back to
// byte-level truncation if the result is not valid JSON.
//
// The byte fallback is UTF-8 safe — it never splits a multi-byte character.
func TruncateResult(result string, maxLen int) string {
	// First pass: slim the JSON to remove low-value fields.
	result = SlimJSON(result)

	// If slimming brought it under the limit, we're done.
	if len(result) <= maxLen {
		return result
	}

	// Second pass: truncate (JSON-aware array shrinking, then byte fallback).
	if truncated, ok := truncateJSON(result, maxLen); ok {
		return truncated
	}

	// Byte fallback — walk backward to avoid splitting a multi-byte UTF-8 character.
	cutPoint := maxLen
	for cutPoint > 0 && !utf8.RuneStart(result[cutPoint]) {
		cutPoint--
	}
	return result[:cutPoint] + fmt.Sprintf("\n...[truncated, %d total bytes]", len(result))
}

// truncateJSON attempts to parse the result as JSON and truncate it intelligently.
// Returns the truncated string and true if successful, or ("", false) if the
// result is not valid JSON or truncation failed.
func truncateJSON(result string, maxLen int) (string, bool) {
	var parsed any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return "", false
	}

	switch v := parsed.(type) {
	case []any:
		return truncateJSONArray(v, maxLen)
	case map[string]any:
		return truncateJSONObject(v, maxLen)
	default:
		return "", false
	}
}

// truncateJSONArray truncates a JSON array by keeping the first N elements
// that fit within maxLen, plus a metadata element indicating truncation.
func truncateJSONArray(arr []any, maxLen int) (string, bool) {
	total := len(arr)
	if total == 0 {
		return "", false
	}

	// Binary search for the maximum number of elements that fit.
	// lo = minimum elements to keep (0), hi = all elements.
	lo, hi := 0, total
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if tryMarshalArray(arr[:mid], total, maxLen) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	keep := lo
	if keep == total {
		// All elements fit — this shouldn't happen since we already checked
		// len(result) > maxLen, but handle it gracefully.
		return "", false
	}

	// Build the truncated array with metadata element.
	truncated := make([]any, keep+1)
	copy(truncated[:keep], arr[:keep])
	truncated[keep] = map[string]any{
		"_truncated": fmt.Sprintf("Showing %d of %d items (result exceeded size limit)", keep, total),
	}

	out, err := json.Marshal(truncated)
	if err != nil {
		return "", false
	}

	s := string(out)
	if len(s) > maxLen {
		// Even with minimal elements, the result is too large (individual elements are huge).
		// Fall back to byte truncation.
		return "", false
	}

	return s, true
}

// tryMarshalArray checks whether keeping n elements of arr (plus a truncation
// metadata element) would fit within maxLen characters.
func tryMarshalArray(elements []any, total int, maxLen int) bool {
	n := len(elements)
	truncated := make([]any, n+1)
	copy(truncated[:n], elements)
	truncated[n] = map[string]any{
		"_truncated": fmt.Sprintf("Showing %d of %d items (result exceeded size limit)", n, total),
	}

	out, err := json.Marshal(truncated)
	if err != nil {
		return false
	}
	return len(string(out)) <= maxLen
}

// truncateJSONObject attempts to truncate large arrays within a JSON object's
// top-level values. If any value is an array with more than 5 elements, it is
// truncated to fit. If the object is still too large after truncation, returns false.
func truncateJSONObject(obj map[string]any, maxLen int) (string, bool) {
	modified := false

	for key, val := range obj {
		arr, ok := val.([]any)
		if !ok || len(arr) <= 5 {
			continue
		}

		// Estimate a per-array budget: give each large array an equal share
		// of the remaining space. This is approximate but avoids over-allocating
		// to one array at the expense of others.
		truncatedArr, ok := truncateObjectArray(arr, maxLen)
		if ok {
			obj[key] = truncatedArr
			modified = true
		}
	}

	if !modified {
		return "", false
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return "", false
	}

	s := string(out)
	if len(s) > maxLen {
		// Object is still too large even after truncating arrays.
		return "", false
	}

	return s, true
}

// truncateObjectArray truncates an array that is a value within a JSON object.
// It keeps as many elements as possible while leaving room for the rest of the
// object (estimated conservatively).
func truncateObjectArray(arr []any, maxLen int) ([]any, bool) {
	total := len(arr)

	// Binary search for the right number of elements.
	lo, hi := 0, total
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if tryMarshalArray(arr[:mid], total, maxLen) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	keep := lo
	if keep == total {
		// All elements fit — no truncation needed for this array.
		return nil, false
	}
	if keep == 0 {
		// Can't even fit one element.
		return []any{
			map[string]any{
				"_truncated": fmt.Sprintf("Showing 0 of %d items (result exceeded size limit)", total),
			},
		}, true
	}

	truncated := make([]any, keep+1)
	copy(truncated[:keep], arr[:keep])
	truncated[keep] = map[string]any{
		"_truncated": fmt.Sprintf("Showing %d of %d items (result exceeded size limit)", keep, total),
	}

	return truncated, true
}
