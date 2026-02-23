package job

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BlockerQuestion is a single question posed by a blocked team, optionally
// with a set of pre-defined answer choices.
type BlockerQuestion struct {
	Text    string
	Options []string // empty = free-form answer
	Answer  string
}

// Blocker represents a BLOCKER.md file written by a team that needs operator
// input before it can continue.
type Blocker struct {
	Team           string
	BlockerSummary string
	Context        string
	WhatWasTried   string
	WhatIsNeeded   string
	Questions      []BlockerQuestion
	Answered       bool
	RawBody        string
}

// ReadBlocker reads BLOCKER.md from jobDir and returns a parsed Blocker.
// Returns nil, nil if the file does not exist or has no blocker: frontmatter
// field (i.e. is not a valid blocker file).
func ReadBlocker(jobDir string) (*Blocker, error) {
	path := filepath.Join(jobDir, "BLOCKER.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading BLOCKER.md: %w", err)
	}

	raw := string(data)
	b := &Blocker{RawBody: raw}

	// Split YAML frontmatter from body.
	fmLines, body, ok := splitFrontmatter(raw)
	if !ok {
		// No frontmatter at all — not a valid blocker file.
		return nil, nil
	}

	// Parse frontmatter: only care about team: and blocker: keys.
	hasBlocker := false
	for _, line := range fmLines {
		if v := strings.TrimPrefix(line, "team:"); v != line {
			b.Team = strings.TrimSpace(v)
		} else if v := strings.TrimPrefix(line, "blocker:"); v != line {
			b.BlockerSummary = strings.TrimSpace(v)
			hasBlocker = true
		}
	}

	if !hasBlocker {
		return nil, nil
	}

	// Parse body sections split on "\n## " headings.
	sections := splitSections(body)
	b.Context = sections["Context"]
	b.WhatWasTried = sections["What Was Tried"]
	b.WhatIsNeeded = sections["What Is Needed"]

	// Parse questions from WhatIsNeeded.
	b.Questions = parseQuestions(b.WhatIsNeeded)

	return b, nil
}

// WriteBlockerAnswers appends a "## User Responses" section to BLOCKER.md
// containing the answered questions.
func WriteBlockerAnswers(jobDir string, b *Blocker) error {
	path := filepath.Join(jobDir, "BLOCKER.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening BLOCKER.md for append: %w", err)
	}
	defer f.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(f, "\n## User Responses\n_Answered: %s_\n\n", ts); err != nil {
		return fmt.Errorf("writing User Responses header: %w", err)
	}

	for _, q := range b.Questions {
		if q.Answer == "" {
			continue
		}
		if _, err := fmt.Fprintf(f, "**Question:** %s\n**Answer:** %s\n\n", q.Text, q.Answer); err != nil {
			return fmt.Errorf("writing question/answer: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing BLOCKER.md: %w", err)
	}
	return nil
}

// splitFrontmatter splits content into frontmatter lines and body text.
// Returns (fmLines, body, true) on success, or ("", "", false) if no valid
// frontmatter block is found.
func splitFrontmatter(content string) ([]string, string, bool) {
	lines := strings.Split(content, "\n")

	start := -1
	for i, l := range lines {
		if l == "---" {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, "", false
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, "", false
	}

	fmLines := lines[start+1 : end]
	body := strings.Join(lines[end+1:], "\n")
	return fmLines, body, true
}

// splitSections parses a markdown body into a map of heading → content by
// splitting on "\n## " section headings.
func splitSections(body string) map[string]string {
	sections := make(map[string]string)

	// Normalise: ensure we can match a leading "## " at the start of the body.
	normalised := "\n" + strings.TrimLeft(body, "\n")
	parts := strings.Split(normalised, "\n## ")

	for _, part := range parts[1:] { // skip the preamble before the first heading
		idx := strings.Index(part, "\n")
		if idx == -1 {
			// Heading with no content.
			heading := strings.TrimSpace(part)
			sections[heading] = ""
			continue
		}
		heading := strings.TrimSpace(part[:idx])
		content := strings.TrimSpace(part[idx+1:])
		sections[heading] = content
	}

	return sections
}

// parseQuestions builds a []BlockerQuestion from the WhatIsNeeded section text.
// Lines starting with "- " are treated as options for the preceding question.
// Non-empty, non-option lines are question text.
// If no bullet options appear anywhere, the entire section is a single
// free-form question.
func parseQuestions(text string) []BlockerQuestion {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	lines := strings.Split(text, "\n")

	var questions []BlockerQuestion
	hasOptions := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			// Option for the most recent question.
			option := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if len(questions) == 0 {
				// Options with no preceding question — create an implicit one.
				questions = append(questions, BlockerQuestion{})
			}
			questions[len(questions)-1].Options = append(questions[len(questions)-1].Options, option)
			hasOptions = true
		} else {
			// New question text.
			questions = append(questions, BlockerQuestion{Text: trimmed})
		}
	}

	// If no options were found at all, collapse everything into a single
	// free-form question whose text is the full section.
	if !hasOptions {
		return []BlockerQuestion{{Text: strings.TrimSpace(text)}}
	}

	return questions
}
