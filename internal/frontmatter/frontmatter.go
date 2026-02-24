// Package frontmatter provides functions for splitting and parsing
// YAML-style frontmatter delimited by "---" lines in Markdown files.
package frontmatter

import (
	"errors"
	"strings"
)

// Split splits content into the frontmatter lines between the opening and
// closing "---" delimiters, and the body text that follows the closing
// delimiter. Both delimiters must be exact line matches (the line is exactly
// "---" with no leading or trailing characters).
//
// Returns an error if the opening delimiter is missing or if the opening
// delimiter is found but no closing delimiter follows it.
func Split(content string) ([]string, string, error) {
	lines := strings.Split(content, "\n")

	// Find opening "---".
	start := -1
	for i, l := range lines {
		if l == "---" {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, "", errors.New("no frontmatter delimiter found")
	}

	// Find closing "---".
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, "", errors.New("frontmatter closing delimiter not found")
	}

	fmLines := lines[start+1 : end]
	body := strings.Join(lines[end+1:], "\n")
	return fmLines, body, nil
}

// Parse splits content into a map of key-value pairs extracted from the
// frontmatter block, plus the body text after the closing delimiter.
//
// It calls [Split] to obtain the raw frontmatter lines, then parses each
// non-empty line by splitting on ": " (colon-space). Lines with both a key
// and value produce kv[key] = value. Lines with only a key (no ": " separator)
// are stored as kv[key] = "".
func Parse(content string) (map[string]string, string, error) {
	fmLines, body, err := Split(content)
	if err != nil {
		return nil, "", err
	}

	kv := make(map[string]string, len(fmLines))
	for _, l := range fmLines {
		if l == "" {
			continue
		}
		parts := strings.SplitN(l, ": ", 2)
		if len(parts) != 2 {
			key := strings.TrimSuffix(strings.TrimSpace(l), ":")
			kv[key] = ""
			continue
		}
		kv[strings.TrimSpace(parts[0])] = parts[1]
	}

	return kv, body, nil
}
