package loader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// openTestStore creates a SQLite store in a temp directory.
func openTestStore(t *testing.T) db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// writeFile creates a file with the given content, creating parent dirs as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

const orchestrationSkillMD = `---
name: Orchestration
description: Skill for orchestrating work
---
Orchestration instructions here.
`

const goDevSkillMD = `---
name: Go Development
description: Go development best practices
tools:
  - go_build
  - go_test
---
Go development skill content.
`

func TestLoad_SystemSkills(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "system", "skills", "orchestration.md"), orchestrationSkillMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].ID != "orchestration" {
		t.Errorf("skill ID = %q, want %q", skills[0].ID, "orchestration")
	}
	if skills[0].Source != "system" {
		t.Errorf("skill source = %q, want %q", skills[0].Source, "system")
	}
}

func TestLoad_UserSkills(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify skill.
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].ID != "go-development" {
		t.Errorf("skill ID = %q, want %q", skills[0].ID, "go-development")
	}
	if skills[0].Source != "user" {
		t.Errorf("skill source = %q, want %q", skills[0].Source, "user")
	}
	if skills[0].Prompt != "Go development skill content." {
		t.Errorf("skill prompt = %q, want %q", skills[0].Prompt, "Go development skill content.")
	}
}

func TestLoad_Idempotent(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)

	l := New(store, configDir)

	// Load twice.
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load (1st): %v", err)
	}
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load (2nd): %v", err)
	}

	// Verify same data after second load.
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after idempotent load, got %d", len(skills))
	}
}

func TestLoad_EmptyDirs(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Create empty directories.
	for _, dir := range []string{
		filepath.Join(configDir, "system", "skills"),
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "graphs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating dir %s: %v", dir, err)
		}
	}

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	skills, _ := store.ListSkills(ctx)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestLoad_NoDirs(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Don't create any directories — configDir exists but is empty.
	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load with no dirs: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Go Development", "go-development"},
		{"Senior Go Dev", "senior-go-dev"},
		{"Blocker Handler", "blocker-handler"},
		{"simple", "simple"},
		{"UPPER CASE", "upper-case"},
		{"with---multiple---hyphens", "with-multiple-hyphens"},
		{"  leading trailing  ", "leading-trailing"},
		{"special!@#$chars", "specialchars"},
		{"mixed Special-Chars_123", "mixed-special-chars123"},
		{"", ""},
		{"already-slugified", "already-slugified"},
		{"-leading-hyphen", "leading-hyphen"},
		{"trailing-hyphen-", "trailing-hyphen"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoad_FullIntegration(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// System + user skills.
	writeFile(t, filepath.Join(configDir, "system", "skills", "orchestration.md"), orchestrationSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	skills, _ := store.ListSkills(ctx)
	if len(skills) != 2 { // orchestration + go-development
		t.Errorf("expected 2 skills, got %d", len(skills))
	}
}

func TestLoad_UnparseableFileSkipped(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Write a valid skill and an invalid one.
	writeFile(t, filepath.Join(configDir, "user", "skills", "good.md"), goDevSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "skills", "bad.md"), "this is not valid frontmatter at all")

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Only the good skill should be loaded.
	skills, _ := store.ListSkills(ctx)
	if len(skills) != 1 {
		t.Errorf("expected 1 skill (bad skipped), got %d", len(skills))
	}
}

func TestLoad_UserSkillShadowsSystem(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// System skill.
	writeFile(t, filepath.Join(configDir, "system", "skills", "orchestration.md"), orchestrationSkillMD)

	// User skill with the same name — should shadow the system one.
	userOrchMD := `---
name: Orchestration
description: Custom orchestration skill
---
Custom orchestration instructions.
`
	writeFile(t, filepath.Join(configDir, "user", "skills", "orchestration.md"), userOrchMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (user shadows system), got %d", len(skills))
	}
	if skills[0].ID != "orchestration" {
		t.Errorf("skill ID = %q, want %q", skills[0].ID, "orchestration")
	}
	if skills[0].Source != "user" {
		t.Errorf("skill source = %q, want %q (user should shadow system)", skills[0].Source, "user")
	}
	if skills[0].Prompt != "Custom orchestration instructions." {
		t.Errorf("skill prompt = %q, want %q", skills[0].Prompt, "Custom orchestration instructions.")
	}
}
