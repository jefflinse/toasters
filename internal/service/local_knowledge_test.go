package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
)

// newTestKnowledgeJob creates a real sqlite-backed LocalService and a job
// row pointing at a fresh temp workspace directory, returning the service
// and the workspace dir so the test can seed .toasters/notes/ files
// directly (mirroring what internal/runtime's job_note_write tool writes).
func newTestKnowledgeJob(t *testing.T) (*LocalService, string, string) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := newTestService(t)
	svc.cfg.Store = store

	workDir := t.TempDir()
	jobID := "job-kb-1"
	if err := store.CreateJob(context.Background(), &db.Job{
		ID: jobID, Title: "KB test job", Type: "test", Status: db.JobStatusActive, WorkspaceDir: workDir,
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return svc, jobID, workDir
}

// writeNote writes a note file directly under workDir/.toasters/notes/,
// mirroring the filename shape internal/runtime/tools.go's noteFilename
// mints: <ts>-<source>-<slug>-<6hex>.md. mtime, when non-zero, is applied
// via os.Chtimes so ordering tests are deterministic.
func writeNote(t *testing.T, workDir, filename, content string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(workDir, ".toasters", "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write note %s: %v", filename, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", filename, err)
		}
	}
}

func TestListJobNotes_ReturnsNewestFirstWithParsedFields(t *testing.T) {
	t.Parallel()

	svc, jobID, workDir := newTestKnowledgeJob(t)
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()

	writeNote(t, workDir, "20260101-090000.000-backend-worker-fixed-the-bug-abc123.md",
		"# Fixed the bug\n\nRoot cause was a race.", older)
	writeNote(t, workDir, "20260102-100000.000-frontend-worker-added-tests-def456.md",
		"# Added tests\n\nCovers the regression.", newer)

	notes, err := svc.Knowledge().ListJobNotes(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListJobNotes: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(notes))
	}

	// Newest first.
	if notes[0].ID != "20260102-100000.000-frontend-worker-added-tests-def456" {
		t.Errorf("notes[0].ID = %q, want the newer note first", notes[0].ID)
	}
	if notes[0].Title != "Added tests" {
		t.Errorf("notes[0].Title = %q, want %q", notes[0].Title, "Added tests")
	}
	if notes[0].Source != "frontend" {
		t.Errorf("notes[0].Source = %q, want %q", notes[0].Source, "frontend")
	}
	if notes[0].Size <= 0 {
		t.Errorf("notes[0].Size = %d, want > 0", notes[0].Size)
	}

	if notes[1].ID != "20260101-090000.000-backend-worker-fixed-the-bug-abc123" {
		t.Errorf("notes[1].ID = %q, want the older note second", notes[1].ID)
	}
	if notes[1].Title != "Fixed the bug" {
		t.Errorf("notes[1].Title = %q, want %q", notes[1].Title, "Fixed the bug")
	}
	if notes[1].Source != "backend" {
		t.Errorf("notes[1].Source = %q, want %q", notes[1].Source, "backend")
	}
}

func TestListJobNotes_EmptyNotesDirIsNotAnError(t *testing.T) {
	t.Parallel()

	svc, jobID, _ := newTestKnowledgeJob(t)
	// No .toasters/notes/ directory created at all.

	notes, err := svc.Knowledge().ListJobNotes(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ListJobNotes: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("got %d notes, want 0", len(notes))
	}
}

func TestListJobNotes_JobNotFound(t *testing.T) {
	t.Parallel()

	svc, _, _ := newTestKnowledgeJob(t)

	_, err := svc.Knowledge().ListJobNotes(context.Background(), "no-such-job")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want wrapping ErrNotFound", err)
	}
}

func TestReadJobNote_ReturnsFullContent(t *testing.T) {
	t.Parallel()

	svc, jobID, workDir := newTestKnowledgeJob(t)
	content := "# Fixed the bug\n\nRoot cause was a race condition in the scheduler."
	writeNote(t, workDir, "20260101-090000.000-backend-worker-fixed-the-bug-abc123.md", content, time.Now())

	got, err := svc.Knowledge().ReadJobNote(context.Background(), jobID, "20260101-090000.000-backend-worker-fixed-the-bug-abc123")
	if err != nil {
		t.Fatalf("ReadJobNote: %v", err)
	}
	if got != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestReadJobNote_TrailingMdExtensionTolerated(t *testing.T) {
	t.Parallel()

	svc, jobID, workDir := newTestKnowledgeJob(t)
	content := "# Note\n\nBody."
	writeNote(t, workDir, "20260101-090000.000-worker-note-abc123.md", content, time.Now())

	got, err := svc.Knowledge().ReadJobNote(context.Background(), jobID, "20260101-090000.000-worker-note-abc123.md")
	if err != nil {
		t.Fatalf("ReadJobNote: %v", err)
	}
	if got != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestReadJobNote_NotFound(t *testing.T) {
	t.Parallel()

	svc, jobID, _ := newTestKnowledgeJob(t)

	_, err := svc.Knowledge().ReadJobNote(context.Background(), jobID, "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want wrapping ErrNotFound", err)
	}
}

func TestReadJobNote_JobNotFound(t *testing.T) {
	t.Parallel()

	svc, _, _ := newTestKnowledgeJob(t)

	_, err := svc.Knowledge().ReadJobNote(context.Background(), "no-such-job", "whatever")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want wrapping ErrNotFound", err)
	}
}

// TestReadJobNote_RejectsPathTraversal is the security-critical case: id
// must never be allowed to escape the notes directory via ".." or an
// embedded path separator, even though the note was legitimately written by
// a worker session elsewhere in the same job's workspace.
func TestReadJobNote_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	svc, jobID, workDir := newTestKnowledgeJob(t)

	// Plant a secret file outside the notes dir that traversal would reach.
	secretPath := filepath.Join(workDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("do not leak"), 0o644); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	traversalIDs := []string{
		"../secret",
		"../../secret",
		"foo/../../secret",
		"sub/note",
	}
	for _, id := range traversalIDs {
		t.Run(id, func(t *testing.T) {
			_, err := svc.Knowledge().ReadJobNote(context.Background(), jobID, id)
			if err == nil {
				t.Fatalf("ReadJobNote(%q): expected error, got nil", id)
			}
			if errors.Is(err, ErrNotFound) {
				// Acceptable too, so long as it didn't succeed — but our
				// implementation rejects these before ever touching the
				// filesystem, so it should be ErrInvalid.
				t.Errorf("ReadJobNote(%q): got ErrNotFound, want ErrInvalid (traversal should be rejected before any file lookup)", id)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("ReadJobNote(%q) err = %v, want wrapping ErrInvalid", id, err)
			}
		})
	}
}
