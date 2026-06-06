package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func seedBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	writeFile(t, base, "main.go", "package main")
	writeFile(t, base, "pkg/util.go", "package pkg")
	writeFile(t, base, "README.md", "hello")
	writeFile(t, base, ".git/HEAD", "ref: refs/heads/main") // must survive promotion
	return base
}

func TestIsolate_IndependentCopies(t *testing.T) {
	base := seedBase(t)

	dirs, cleanup, err := Isolate(base, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if len(dirs) != 3 {
		t.Fatalf("got %d dirs, want 3", len(dirs))
	}
	for _, d := range dirs {
		if got, ok := readFile(t, d, "main.go"); !ok || got != "package main" {
			t.Errorf("%s: main.go = %q, ok=%v", d, got, ok)
		}
		if got, ok := readFile(t, d, "pkg/util.go"); !ok || got != "package pkg" {
			t.Errorf("%s: pkg/util.go = %q, ok=%v", d, got, ok)
		}
		if _, ok := readFile(t, d, ".git/HEAD"); !ok {
			t.Errorf("%s: .git was not copied", d)
		}
	}

	// Mutating one branch must not affect another or the base.
	writeFile(t, dirs[0], "main.go", "MUTATED")
	if got, _ := readFile(t, dirs[1], "main.go"); got != "package main" {
		t.Errorf("branch 1 leaked branch 0's mutation: %q", got)
	}
	if got, _ := readFile(t, base, "main.go"); got != "package main" {
		t.Errorf("base leaked a branch mutation: %q", got)
	}
}

func TestIsolate_CleanupRemovesAll(t *testing.T) {
	base := seedBase(t)
	dirs, cleanup, err := Isolate(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(dirs[0])
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temp root still exists after cleanup: %v", err)
	}
}

func TestIsolate_ZeroOrNegative(t *testing.T) {
	base := seedBase(t)
	for _, n := range []int{0, -1} {
		dirs, cleanup, err := Isolate(base, n)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(dirs) != 0 {
			t.Fatalf("n=%d: got %d dirs, want 0", n, len(dirs))
		}
		cleanup() // must not panic
	}
}

func TestPromote_MirrorsWinnerExcludingGit(t *testing.T) {
	base := seedBase(t)
	dirs, cleanup, err := Isolate(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	winner := dirs[0]

	// Winner: edit a file, add a nested file, delete a file, and rewrite its
	// own .git (which must NOT be promoted over base's).
	writeFile(t, winner, "main.go", "package main // winner")
	writeFile(t, winner, "pkg/new.go", "package pkg // added")
	if err := os.Remove(filepath.Join(winner, "README.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, winner, ".git/HEAD", "ref: refs/heads/branch")

	if err := Promote(winner, base); err != nil {
		t.Fatal(err)
	}

	if got, _ := readFile(t, base, "main.go"); got != "package main // winner" {
		t.Errorf("edited file not promoted: %q", got)
	}
	if got, ok := readFile(t, base, "pkg/new.go"); !ok || got != "package pkg // added" {
		t.Errorf("added nested file not promoted: %q ok=%v", got, ok)
	}
	if _, ok := readFile(t, base, "README.md"); ok {
		t.Error("deleted file still present in base after promote")
	}
	// base's own .git must be untouched.
	if got, _ := readFile(t, base, ".git/HEAD"); got != "ref: refs/heads/main" {
		t.Errorf("base .git was overwritten by winner: %q", got)
	}
}

func TestPromote_RemovesNestedStaleFiles(t *testing.T) {
	base := t.TempDir()
	writeFile(t, base, "a/b/stale.go", "stale")
	writeFile(t, base, "a/keep.go", "keep")

	winner := t.TempDir()
	writeFile(t, winner, "a/keep.go", "keep")

	if err := Promote(winner, base); err != nil {
		t.Fatal(err)
	}
	if _, ok := readFile(t, base, "a/b/stale.go"); ok {
		t.Error("nested stale file survived promote")
	}
	if got, ok := readFile(t, base, "a/keep.go"); !ok || got != "keep" {
		t.Errorf("kept file lost: %q ok=%v", got, ok)
	}
}
