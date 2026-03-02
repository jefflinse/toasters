// LLM generation helpers: utility functions for processing LLM-generated content.
package tui

import "strings"

// stripCodeFences removes markdown code fences from LLM output. Some models
// wrap their output in ```yaml or ``` blocks despite being instructed not to.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)

	// Strip opening fence (``` or ```yaml, ```json, etc.).
	if strings.HasPrefix(s, "```") {
		// Find the end of the first line.
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
	}

	// Strip closing fence.
	s = strings.TrimSuffix(s, "```")

	return strings.TrimSpace(s)
}
