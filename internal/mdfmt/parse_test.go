package mdfmt_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jefflinse/toasters/internal/mdfmt"
)

// writeTestFile creates a temporary .md file with the given content and returns its path.
func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test file %s: %v", path, err)
	}
	return path
}

func TestParseSkill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: code-review
description: Reviews code for quality and correctness
tools:
  - read_file
  - grep
---
You are a code review specialist. Review the provided code carefully.`

	path := writeTestFile(t, dir, "code-review.md", content)
	def, err := mdfmt.ParseSkill(path)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}

	if def.Name != "code-review" {
		t.Errorf("Name = %q, want %q", def.Name, "code-review")
	}
	if def.Description != "Reviews code for quality and correctness" {
		t.Errorf("Description = %q, want %q", def.Description, "Reviews code for quality and correctness")
	}
	if !reflect.DeepEqual(def.Tools, []string{"read_file", "grep"}) {
		t.Errorf("Tools = %v, want %v", def.Tools, []string{"read_file", "grep"})
	}
	if def.Body != "You are a code review specialist. Review the provided code carefully." {
		t.Errorf("Body = %q, want %q", def.Body, "You are a code review specialist. Review the provided code carefully.")
	}
}

func TestParseSkill_NameDefaultsToFilenameStem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
description: A nameless skill
---
Do stuff.`

	path := writeTestFile(t, dir, "my-cool-skill.md", content)
	def, err := mdfmt.ParseSkill(path)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if def.Name != "my-cool-skill" {
		t.Errorf("Name = %q, want %q", def.Name, "my-cool-skill")
	}
}

func TestParseSkill_EmptyFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
---
Just a body with empty frontmatter.`

	path := writeTestFile(t, dir, "empty-fm.md", content)
	def, err := mdfmt.ParseSkill(path)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if def.Name != "empty-fm" {
		t.Errorf("Name = %q, want %q", def.Name, "empty-fm")
	}
	if def.Body != "Just a body with empty frontmatter." {
		t.Errorf("Body = %q, want %q", def.Body, "Just a body with empty frontmatter.")
	}
}

func TestParseSkill_MissingFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `No frontmatter here, just text.`

	path := writeTestFile(t, dir, "no-fm.md", content)
	_, err := mdfmt.ParseSkill(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestParseSkill_NonexistentFile(t *testing.T) {
	t.Parallel()
	_, err := mdfmt.ParseSkill("/nonexistent/path/skill.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestParseSkill_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: [invalid yaml
  this is broken: {
---
Body.`

	path := writeTestFile(t, dir, "bad-yaml.md", content)
	_, err := mdfmt.ParseSkill(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestParseBytes(t *testing.T) {
	t.Parallel()

	t.Run("valid skill", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-skill\ndescription: A skill\n---\nSkill body.")
		def, err := mdfmt.ParseBytes(data)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		if def.Name != "test-skill" {
			t.Errorf("Name = %q, want %q", def.Name, "test-skill")
		}
		if def.Body != "Skill body." {
			t.Errorf("Body = %q, want %q", def.Body, "Skill body.")
		}
	})

	t.Run("missing frontmatter", func(t *testing.T) {
		t.Parallel()
		data := []byte("No frontmatter here.")
		_, err := mdfmt.ParseBytes(data)
		if err == nil {
			t.Fatal("expected error for missing frontmatter, got nil")
		}
	})

	t.Run("name empty when parsing bytes without filename", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\ndescription: No name\n---\nBody.")
		def, err := mdfmt.ParseBytes(data)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		if def.Name != "" {
			t.Errorf("Name = %q, want empty", def.Name)
		}
	})
}
