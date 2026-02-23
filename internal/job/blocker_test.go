package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wellFormedBlockerWithOptions is a BLOCKER.md that has multiple-choice questions.
const wellFormedBlockerWithOptions = `---
team: backend
blocker: Cannot determine the correct database schema
---

## Context

The team is implementing the user profile feature but hit an ambiguity in the schema.

## What Was Tried

We reviewed the existing migrations and the product spec, but neither is definitive.

## What Is Needed

Which column type should be used for the avatar field?
- varchar(255)
- text
- bytea

Should the table be partitioned?
- yes
- no
`

// wellFormedBlockerFreeForm is a BLOCKER.md with no bullet options.
const wellFormedBlockerFreeForm = `---
team: frontend
blocker: Need design decision on modal behaviour
---

## Context

The modal close behaviour is undefined in the spec.

## What Was Tried

Checked Figma and Notion — no guidance found.

## What Is Needed

Please describe the expected behaviour when the user clicks outside the modal.
`

func TestReadBlocker_WithOptions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "BLOCKER.md"), []byte(wellFormedBlockerWithOptions), 0644); err != nil {
		t.Fatalf("writing BLOCKER.md: %v", err)
	}

	b, err := ReadBlocker(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil Blocker, got nil")
	}

	if b.Team != "backend" {
		t.Errorf("Team: got %q, want %q", b.Team, "backend")
	}
	if b.BlockerSummary != "Cannot determine the correct database schema" {
		t.Errorf("BlockerSummary: got %q", b.BlockerSummary)
	}
	if !strings.Contains(b.Context, "user profile feature") {
		t.Errorf("Context missing expected text, got: %q", b.Context)
	}
	if !strings.Contains(b.WhatWasTried, "existing migrations") {
		t.Errorf("WhatWasTried missing expected text, got: %q", b.WhatWasTried)
	}

	if len(b.Questions) != 2 {
		t.Fatalf("Questions: got %d, want 2", len(b.Questions))
	}

	q0 := b.Questions[0]
	if q0.Text != "Which column type should be used for the avatar field?" {
		t.Errorf("Questions[0].Text: got %q", q0.Text)
	}
	if len(q0.Options) != 3 {
		t.Errorf("Questions[0].Options: got %d, want 3", len(q0.Options))
	} else {
		wantOpts := []string{"varchar(255)", "text", "bytea"}
		for i, want := range wantOpts {
			if q0.Options[i] != want {
				t.Errorf("Questions[0].Options[%d]: got %q, want %q", i, q0.Options[i], want)
			}
		}
	}

	q1 := b.Questions[1]
	if q1.Text != "Should the table be partitioned?" {
		t.Errorf("Questions[1].Text: got %q", q1.Text)
	}
	if len(q1.Options) != 2 {
		t.Errorf("Questions[1].Options: got %d, want 2", len(q1.Options))
	}
}

func TestReadBlocker_FreeForm(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "BLOCKER.md"), []byte(wellFormedBlockerFreeForm), 0644); err != nil {
		t.Fatalf("writing BLOCKER.md: %v", err)
	}

	b, err := ReadBlocker(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil Blocker, got nil")
	}

	if b.Team != "frontend" {
		t.Errorf("Team: got %q, want %q", b.Team, "frontend")
	}

	if len(b.Questions) != 1 {
		t.Fatalf("Questions: got %d, want 1", len(b.Questions))
	}
	q := b.Questions[0]
	if len(q.Options) != 0 {
		t.Errorf("expected no options for free-form question, got %d", len(q.Options))
	}
	if !strings.Contains(q.Text, "clicks outside the modal") {
		t.Errorf("free-form question text unexpected: %q", q.Text)
	}
}

func TestReadBlocker_FileAbsent(t *testing.T) {
	dir := t.TempDir()

	b, err := ReadBlocker(dir)
	if err != nil {
		t.Fatalf("expected nil error for absent file, got: %v", err)
	}
	if b != nil {
		t.Fatalf("expected nil Blocker for absent file, got: %+v", b)
	}
}

func TestReadBlocker_NoBlockerField(t *testing.T) {
	// A file with frontmatter but no blocker: key should return nil, nil.
	content := "---\nteam: ops\n---\n\n## Context\n\nSome context.\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "BLOCKER.md"), []byte(content), 0644); err != nil {
		t.Fatalf("writing BLOCKER.md: %v", err)
	}

	b, err := ReadBlocker(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b != nil {
		t.Fatalf("expected nil Blocker for file without blocker: field, got: %+v", b)
	}
}

func TestWriteBlockerAnswers(t *testing.T) {
	dir := t.TempDir()
	// Write an initial BLOCKER.md so the file exists for appending.
	initial := "---\nteam: backend\nblocker: needs input\n---\n\n## What Is Needed\n\nWhat approach?\n"
	if err := os.WriteFile(filepath.Join(dir, "BLOCKER.md"), []byte(initial), 0644); err != nil {
		t.Fatalf("writing initial BLOCKER.md: %v", err)
	}

	b := &Blocker{
		Questions: []BlockerQuestion{
			{Text: "What approach?", Answer: "Use the adapter pattern"},
			{Text: "Unanswered question", Answer: ""},
		},
	}

	if err := WriteBlockerAnswers(dir, b); err != nil {
		t.Fatalf("WriteBlockerAnswers: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "BLOCKER.md"))
	if err != nil {
		t.Fatalf("reading BLOCKER.md after write: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## User Responses") {
		t.Error("missing '## User Responses' section")
	}
	if !strings.Contains(content, "_Answered:") {
		t.Error("missing '_Answered:' timestamp line")
	}
	if !strings.Contains(content, "**Question:** What approach?") {
		t.Error("missing question text")
	}
	if !strings.Contains(content, "**Answer:** Use the adapter pattern") {
		t.Error("missing answer text")
	}
	// Unanswered question should not appear.
	if strings.Contains(content, "Unanswered question") {
		t.Error("unanswered question should not appear in output")
	}
}
