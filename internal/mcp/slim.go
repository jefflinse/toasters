package mcp

import (
	"encoding/json"
	"strings"
)

// SlimJSON attempts to parse the result as JSON and remove low-value fields
// to improve information density. If the result is not valid JSON, it is
// returned unchanged.
//
// The slimming is conservative and generic — it targets patterns common
// across REST APIs (HATEOAS links, expanded reference objects, null fields,
// opaque blobs) without any API-specific knowledge.
func SlimJSON(result string) string {
	var parsed any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return result // not JSON, return unchanged
	}

	slimmed := slimValue(parsed)

	out, err := json.Marshal(slimmed)
	if err != nil {
		return result // marshaling failed, return original
	}
	return string(out)
}

// slimValue recursively slims a parsed JSON value.
func slimValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return slimObject(val)
	case []any:
		return slimArray(val)
	default:
		return v
	}
}

// slimArray recursively slims each element.
func slimArray(arr []any) []any {
	result := make([]any, len(arr))
	for i, v := range arr {
		result[i] = slimValue(v)
	}
	return result
}

// slimObject applies all slimming rules to an object and recurses into values.
func slimObject(obj map[string]any) map[string]any {
	// Determine if this is a "primary resource" (has content-bearing fields).
	isPrimary := hasAnyKey(obj, "title", "name", "message", "body", "description", "full_name")

	result := make(map[string]any, len(obj))
	for key, val := range obj {
		// Rule 1: Drop null values.
		if val == nil {
			continue
		}

		// Rule 7: Drop known useless ID fields.
		if key == "node_id" || key == "gravatar_id" {
			continue
		}

		// Rule 2: Drop *_url fields (except html_url on primary resources).
		if strings.HasSuffix(key, "_url") {
			if key == "html_url" && isPrimary {
				result[key] = val
				continue
			}
			if _, ok := val.(string); ok {
				continue
			}
		}

		// Rule 2b: Drop "url" field if it's an HTTP URL.
		if key == "url" {
			if s, ok := val.(string); ok && strings.HasPrefix(s, "http") {
				continue
			}
		}

		// Rule 3: Drop URI template strings (RFC 6570 patterns like "{/path}" or "{?query}").
		// Only match URL-like values to avoid dropping user-authored text containing braces.
		if s, ok := val.(string); ok {
			if strings.HasPrefix(s, "http") && strings.Contains(s, "{") && strings.Contains(s, "}") {
				continue
			}
			// Rule 4: Drop API-domain URLs (machine-navigation links).
			if strings.HasPrefix(s, "https://api.") {
				continue
			}
		}

		// Rule 6: Drop large opaque strings.
		if s, ok := val.(string); ok && len(s) > 500 {
			if isOpaqueBlob(s) {
				continue
			}
		}

		// Recurse into nested values.
		result[key] = slimValue(val)
	}
	return result
}

// hasAnyKey returns true if obj contains at least one of the given keys.
func hasAnyKey(obj map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

// isOpaqueBlob returns true if s looks like a PGP/PEM signature or base64 blob.
// The base64 check is bounded to the first 1024 bytes for performance.
func isOpaqueBlob(s string) bool {
	// PGP/PEM signatures.
	if strings.HasPrefix(s, "-----BEGIN ") {
		return true
	}
	// Base64-like: only alphanumeric, +, /, =, and whitespace.
	// Check a bounded prefix — if the first 1024 bytes are base64-like,
	// the whole string almost certainly is.
	check := len(s)
	if check > 1024 {
		check = 1024
	}
	for i := range check {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') &&
			c != '+' && c != '/' && c != '=' && c != '\n' && c != '\r' && c != ' ' {
			return false
		}
	}
	return true
}
