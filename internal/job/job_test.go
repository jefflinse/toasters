package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeOverviewMD writes a minimal OVERVIEW.md into dir and returns the path.
// The caller controls the frontmatter fields via the content string.
func writeOverviewMD(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "OVERVIEW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing OVERVIEW.md: %v", err)
	}
	return path
}

// readFrontmatter is a test helper that loads the OVERVIEW.md from dir and
// returns the parsed Frontmatter, failing the test on any error.
func readFrontmatter(t *testing.T, dir string) Frontmatter {
	t.Helper()
	j, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after UpdateFrontmatter: %v", err)
	}
	return j.Frontmatter
}

// TestUpdateFrontmatter_SetCompleted verifies that passing a "completed" key
// in the updates map writes the value into the OVERVIEW.md frontmatter.
func TestUpdateFrontmatter_SetCompleted(t *testing.T) {
	dir := t.TempDir()
	writeOverviewMD(t, dir, `---
id: test-job
name: Test Job
description: A test job
status: active
created: 2026-01-01T00:00:00Z
updated: 2026-01-01T00:00:00Z
completed:
---

Some body text.
`)

	const wantCompleted = "2026-01-01T00:00:00Z"
	if err := UpdateFrontmatter(dir, map[string]string{"completed": wantCompleted}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	fm := readFrontmatter(t, dir)
	if fm.Completed != wantCompleted {
		t.Errorf("Completed: got %q, want %q", fm.Completed, wantCompleted)
	}
}

// TestUpdateFrontmatter_UpdatedAlwaysBumped verifies that UpdateFrontmatter
// always advances the "updated" timestamp, even when the change is unrelated.
func TestUpdateFrontmatter_UpdatedAlwaysBumped(t *testing.T) {
	dir := t.TempDir()

	const originalUpdated = "2025-06-15T10:00:00Z"
	writeOverviewMD(t, dir, `---
id: bump-job
name: Bump Job
description: Testing updated bump
status: active
created: 2025-06-15T10:00:00Z
updated: `+originalUpdated+`
completed:
---
`)

	// Parse the original timestamp so we can compare after the call.
	origTime, err := time.Parse(time.RFC3339, originalUpdated)
	if err != nil {
		t.Fatalf("parsing original updated timestamp: %v", err)
	}

	if err := UpdateFrontmatter(dir, map[string]string{"status": "done"}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	fm := readFrontmatter(t, dir)

	if fm.Updated == originalUpdated {
		t.Errorf("updated: expected timestamp to change from %q, but it did not", originalUpdated)
	}

	newTime, err := time.Parse(time.RFC3339, fm.Updated)
	if err != nil {
		t.Errorf("updated %q is not valid RFC3339: %v", fm.Updated, err)
	}

	if !newTime.After(origTime) {
		t.Errorf("updated: new timestamp %q is not after original %q", fm.Updated, originalUpdated)
	}
}

// TestUpdateFrontmatter_NilMapNoOp verifies that passing a nil updates map
// does not clear existing frontmatter fields — in particular, "completed".
func TestUpdateFrontmatter_NilMapNoOp(t *testing.T) {
	dir := t.TempDir()

	const wantCompleted = "2026-01-01T00:00:00Z"
	writeOverviewMD(t, dir, `---
id: nil-map-job
name: Nil Map Job
description: Testing nil map behaviour
status: active
created: 2026-01-01T00:00:00Z
updated: 2026-01-01T00:00:00Z
completed: `+wantCompleted+`
---
`)

	if err := UpdateFrontmatter(dir, nil); err != nil {
		t.Fatalf("UpdateFrontmatter(nil): %v", err)
	}

	fm := readFrontmatter(t, dir)

	// completed must be preserved; updated is allowed to change.
	if fm.Completed != wantCompleted {
		t.Errorf("Completed: got %q, want %q — nil map must not clear existing fields", fm.Completed, wantCompleted)
	}
}
