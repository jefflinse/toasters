package runtime

import (
	"strings"
	"testing"
)

func TestComputeFileChange_NewFile(t *testing.T) {
	fc := computeFileChange("write_file", "foo.txt", "", "line1\nline2\n", true)

	if !fc.Created {
		t.Errorf("Created = false, want true")
	}
	if fc.Added != 2 {
		t.Errorf("Added = %d, want 2", fc.Added)
	}
	if fc.Removed != 0 {
		t.Errorf("Removed = %d, want 0", fc.Removed)
	}
	if fc.ToolName != "write_file" {
		t.Errorf("ToolName = %q, want write_file", fc.ToolName)
	}
	if fc.Path != "foo.txt" {
		t.Errorf("Path = %q, want foo.txt", fc.Path)
	}
	if strings.Contains(fc.Diff, "--- ") || strings.Contains(fc.Diff, "+++ ") {
		t.Errorf("Diff contains file-header lines: %q", fc.Diff)
	}
	if !strings.Contains(fc.Diff, "@@") {
		t.Errorf("Diff missing hunk header: %q", fc.Diff)
	}
}

func TestComputeFileChange_Modification(t *testing.T) {
	// Two changes far enough apart (beyond the default 3-line context) to
	// land in separate hunks, keeping the expected +/- counts unambiguous.
	old := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	new := "line1\nline2\nline3-changed\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nappended\n"

	fc := computeFileChange("edit_file", "bar.txt", old, new, false)

	if fc.Created {
		t.Errorf("Created = true, want false")
	}
	if fc.Added != 2 { // "line3-changed" + "appended"
		t.Errorf("Added = %d, want 2", fc.Added)
	}
	if fc.Removed != 1 { // "line3"
		t.Errorf("Removed = %d, want 1", fc.Removed)
	}
	if strings.Count(fc.Diff, "@@ ") != 2 {
		t.Errorf("Diff should have 2 hunks, got: %q", fc.Diff)
	}
	if strings.Contains(fc.Diff, "--- ") || strings.Contains(fc.Diff, "+++ ") {
		t.Errorf("Diff contains file-header lines: %q", fc.Diff)
	}
}

func TestComputeFileChange_NoOp(t *testing.T) {
	content := "unchanged\ncontent\n"
	fc := computeFileChange("write_file", "same.txt", content, content, false)

	if (fc != FileChange{}) {
		t.Errorf("expected zero FileChange for no-op write, got %+v", fc)
	}
}

func TestComputeFileChange_Truncation(t *testing.T) {
	var oldBuilder, newBuilder strings.Builder
	for i := 0; i < 500; i++ {
		oldBuilder.WriteString("old line\n")
		newBuilder.WriteString("new line\n")
	}

	fc := computeFileChange("write_file", "big.txt", oldBuilder.String(), newBuilder.String(), false)

	if !fc.Truncated {
		t.Errorf("Truncated = false, want true for a 500-line change")
	}
	if lines := strings.Count(fc.Diff, "\n"); lines > maxDiffLines {
		t.Errorf("Diff has %d lines, want <= %d", lines, maxDiffLines)
	}
	if len(fc.Diff) > maxDiffBytes {
		t.Errorf("Diff is %d bytes, want <= %d", len(fc.Diff), maxDiffBytes)
	}
	// Counts reflect the full change, not the capped diff.
	if fc.Added != 500 {
		t.Errorf("Added = %d, want 500 (uncapped)", fc.Added)
	}
	if fc.Removed != 500 {
		t.Errorf("Removed = %d, want 500 (uncapped)", fc.Removed)
	}
}

func TestCapDiff_NoTruncationWhenSmall(t *testing.T) {
	body := "@@ -1 +1 @@\n-old\n+new\n"
	diff, truncated := capDiff(body, maxDiffLines, maxDiffBytes)
	if truncated {
		t.Errorf("truncated = true, want false for a small diff")
	}
	if diff != body {
		t.Errorf("diff = %q, want %q", diff, body)
	}
}

func TestCapDiff_NoFalseTruncationAtExactLimit(t *testing.T) {
	// A body of exactly maxLines newline-terminated lines must not be
	// flagged as truncated — SplitAfter's trailing empty element (from the
	// final "\n") must not be mistaken for a dropped line.
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString("line\n")
	}
	body := b.String()

	diff, truncated := capDiff(body, 5, maxDiffBytes)
	if truncated {
		t.Errorf("truncated = true, want false for a body with exactly maxLines lines")
	}
	if diff != body {
		t.Errorf("diff = %q, want %q", diff, body)
	}
}
