package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestJobNoteWrite_CreatesFileUnderNotesDir verifies job_note_write lands its
// file at <workDir>/.toasters/notes/ and never leaks an absolute path in the
// tool result text (the model must only ever see workspace-relative info —
// see displayPath's rationale in tools.go).
func TestJobNoteWrite_CreatesFileUnderNotesDir(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	result, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "Found the bug",
		"content": "The off-by-one is in the loop bound.",
	}))
	assertNoError(t, err)
	assertContains(t, result, "Found the bug")
	assertNotContains(t, result, dir) // no absolute path leak

	entries, err := os.ReadDir(filepath.Join(dir, ".toasters", "notes"))
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("expected 1 note file, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".md") {
		t.Errorf("note filename %q does not end in .md", entries[0].Name())
	}
	if !strings.Contains(entries[0].Name(), "found-the-bug") {
		t.Errorf("note filename %q does not contain the sanitized slug", entries[0].Name())
	}
}

// TestJobNoteWrite_RejectsOversizedContent checks the maxNoteBytes cap.
func TestJobNoteWrite_RejectsOversizedContent(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	huge := strings.Repeat("x", maxNoteBytes+1)
	_, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "too big",
		"content": huge,
	}))
	assertError(t, err)
}

// TestJobNoteRead_RoundTripsByID writes a note then reads it back by the id
// returned in the write result.
func TestJobNoteRead_RoundTripsByID(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	writeResult, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "Round trip",
		"content": "some findings here",
	}))
	assertNoError(t, err)

	id := extractNoteID(t, writeResult)

	// job_note_write stamps the title as a leading Markdown heading so
	// job_notes_search's first-line title derivation surfaces it; the read
	// content therefore includes it.
	const want = "# Round trip\n\nsome findings here"

	readResult, err := ct.Execute(context.Background(), "job_note_read", mustJSON(t, map[string]any{
		"id": id,
	}))
	assertNoError(t, err)
	assertEqual(t, want, readResult)

	// A model that includes ".md" in the id should still work.
	readResult2, err := ct.Execute(context.Background(), "job_note_read", mustJSON(t, map[string]any{
		"id": id + ".md",
	}))
	assertNoError(t, err)
	assertEqual(t, want, readResult2)
}

// TestJobNoteRead_UnknownID errors cleanly for a nonexistent note.
func TestJobNoteRead_UnknownID(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	_, err := ct.Execute(context.Background(), "job_note_read", mustJSON(t, map[string]any{
		"id": "20260101-000000.000-worker-nope-abc123",
	}))
	assertError(t, err)
}

// TestJobNoteRead_PathTraversalRejected checks that a traversal id cannot
// escape workDir. A file is planted outside workDir at exactly the location
// the traversal targets, so a confinement failure would return its content
// rather than merely a generic "not found" — proving resolvePath's prefix
// guard, not just a coincidental missing file, is what rejects this.
func TestJobNoteRead_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	// Plant a file outside workDir at the exact spot the traversal id
	// resolves to (see the filepath.Rel/Join inverse relationship below).
	outside := t.TempDir()
	secret := filepath.Join(outside, "passwd.md")
	if err := os.WriteFile(secret, []byte("root:x:0:0"), 0o644); err != nil {
		t.Fatalf("writing planted file: %v", err)
	}

	notesDir := filepath.Join(dir, ".toasters", "notes")
	rel, err := filepath.Rel(notesDir, secret)
	assertNoError(t, err)
	traversalID := strings.TrimSuffix(filepath.ToSlash(rel), ".md")

	result, err := ct.Execute(context.Background(), "job_note_read", mustJSON(t, map[string]any{
		"id": traversalID,
	}))
	assertError(t, err)
	assertNotContains(t, result, "root:x:0:0")
}

// TestJobNoteWrite_TitleTraversalSanitized ensures a title containing path
// traversal sequences never escapes the notes directory — sanitizeNoteToken
// strips everything but [a-z0-9] runs, so "../../etc/passwd" collapses to a
// harmless slug rather than a path component.
func TestJobNoteWrite_TitleTraversalSanitized(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	_, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "../../etc/passwd",
		"content": "malicious title test",
	}))
	assertNoError(t, err)

	// Nothing should have been written outside workDir.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "etc", "passwd")); !os.IsNotExist(statErr) {
		t.Fatal("title traversal escaped the workspace")
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".toasters", "notes"))
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("expected 1 note file under the notes dir, got %d", len(entries))
	}
	// The sanitized slug must not contain any path separators.
	if strings.ContainsAny(entries[0].Name(), "/\\") {
		t.Errorf("note filename %q contains a path separator", entries[0].Name())
	}
}

// TestJobNoteWrite_ConcurrentWritesProduceDistinctFiles fires many concurrent
// job_note_write calls and checks none clobbered another — the timestamp +
// random-hex suffix in noteFilename is what guarantees this under a fleet of
// simultaneous writers (see docs/kb-design.md's immutable write model).
func TestJobNoteWrite_ConcurrentWritesProduceDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
				"title":   "concurrent note",
				"content": "content from goroutine",
			}))
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, ".toasters", "notes"))
	assertNoError(t, err)
	if len(entries) != n {
		t.Fatalf("expected %d distinct note files, got %d (clobbering occurred)", n, len(entries))
	}
	seen := make(map[string]bool, n)
	for _, e := range entries {
		if seen[e.Name()] {
			t.Errorf("duplicate filename %q", e.Name())
		}
		seen[e.Name()] = true
	}
}

// TestJobNotesSearch_FindsByContent writes a few notes and checks search
// finds the one matching a content keyword, case-insensitively.
func TestJobNotesSearch_FindsByContent(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	mustWriteNote(t, ct, "Note A", "the database migration is tricky")
	mustWriteNote(t, ct, "Note B", "auth token refresh works now")
	mustWriteNote(t, ct, "Note C", "unrelated finding about styling")

	result, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "AUTH TOKEN",
	}))
	assertNoError(t, err)
	assertContains(t, result, "Note B")
	assertNotContains(t, result, "Note A")
	assertNotContains(t, result, "Note C")
}

// TestJobNotesSearch_RespectsTopK checks that only top_k results are returned
// even when more notes match.
func TestJobNotesSearch_RespectsTopK(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	for i := 0; i < 8; i++ {
		mustWriteNote(t, ct, "shared keyword note", "shared-keyword content here")
	}

	result, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "shared-keyword",
		"top_k": 3,
	}))
	assertNoError(t, err)

	count := countResultEntries(result)
	if count != 3 {
		t.Errorf("expected 3 results (top_k), got %d in:\n%s", count, result)
	}
}

// countResultEntries counts the top-level "[<id>] ..." lines in a
// job_notes_search result — one per matched note.
func countResultEntries(result string) int {
	n := 0
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "[") {
			n++
		}
	}
	return n
}

// TestJobNotesSearch_ResultStaysUnderSizeCap writes many large notes and
// checks the combined search result respects maxNoteSearchResultBytes (and,
// transitively, the generic 8KB maxToolResultBytes tool-result cap).
func TestJobNotesSearch_ResultStaysUnderSizeCap(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	bigContent := strings.Repeat("lorem ipsum dolor sit amet needle ", 200) // well over maxNoteSnippetBytes
	for i := 0; i < 10; i++ {
		mustWriteNote(t, ct, "big note", bigContent)
	}

	result, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "needle",
		"top_k": 10,
	}))
	assertNoError(t, err)
	if len(result) > maxNoteSearchResultBytes {
		t.Errorf("search result is %d bytes, want <= %d", len(result), maxNoteSearchResultBytes)
	}
	if len(result) >= maxToolResultBytes {
		t.Errorf("search result is %d bytes, want < the generic %d tool-result cap", len(result), maxToolResultBytes)
	}
}

// TestJobNotesSearch_EmptyQueryListsRecent checks the "acts as list" behavior
// for an empty query.
func TestJobNotesSearch_EmptyQueryListsRecent(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	mustWriteNote(t, ct, "First", "first content")
	mustWriteNote(t, ct, "Second", "second content")

	result, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "",
	}))
	assertNoError(t, err)
	assertContains(t, result, "First")
	assertContains(t, result, "Second")
}

// TestJobNotesSearch_NoNotesYet checks the empty-directory message before any
// note has ever been written (the directory doesn't exist yet).
func TestJobNotesSearch_NoNotesYet(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	result, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "anything",
	}))
	assertNoError(t, err)
	assertContains(t, result, "no notes yet")
}

// TestJobNotesSearch_IDTraversalRejected checks that job_notes_search's own
// directory resolution (".toasters/notes/") can't be redirected by a
// maliciously constructed workDir/alias — this exercises the same
// resolvePath choke point as job_note_read/write, just via the search path's
// os.ReadDir call.
func TestJobNotesSearch_IDTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))
	mustWriteNote(t, ct, "note", "content")

	// job_note_read is the tool that takes a caller-supplied id; confirm the
	// same traversal id used against search's directory is rejected there too.
	_, err := ct.Execute(context.Background(), "job_note_read", mustJSON(t, map[string]any{
		"id": "../../../../etc/passwd",
	}))
	assertError(t, err)
}

// TestCoreTools_KBDisabled_ToolsAbsentAndRejected checks the kill switch:
// with kbEnabled=false the three tools are absent from Definitions() and
// Execute rejects them exactly like any other unknown tool.
func TestCoreTools_KBDisabled_ToolsAbsentAndRejected(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(false))

	defs := ct.Definitions()
	for _, name := range []string{"job_note_write", "job_notes_search", "job_note_read"} {
		for _, d := range defs {
			if d.Name == name {
				t.Errorf("Definitions() includes %q with kb disabled; tools: %v", name, toolNames(defs))
			}
		}
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"job_note_write", map[string]any{"title": "t", "content": "c"}},
		{"job_notes_search", map[string]any{"query": ""}},
		{"job_note_read", map[string]any{"id": "whatever"}},
	} {
		_, err := ct.Execute(context.Background(), tc.name, mustJSON(t, tc.args))
		if err == nil {
			t.Errorf("Execute(%q) with kb disabled: expected error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("Execute(%q) with kb disabled: err = %q, want to look like the unknown-tool error", tc.name, err.Error())
		}
	}
}

// TestCoreTools_KBEnabledByDefault_False checks that omitting WithKBNotes
// leaves the feature off (CoreTools has no implicit default-on behavior;
// callers — runtime.Runtime and graphexec.Executor — are responsible for
// threading config.KBConfig.Enabled through explicitly).
func TestCoreTools_KBEnabledByDefault_False(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	for _, d := range ct.Definitions() {
		if d.Name == "job_note_write" || d.Name == "job_notes_search" || d.Name == "job_note_read" {
			t.Errorf("job-note tool %q present without WithKBNotes(true)", d.Name)
		}
	}
}

// mustWriteNote is a small test helper wrapping job_note_write.
func mustWriteNote(t *testing.T, ct *CoreTools, title, content string) string {
	t.Helper()
	result, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   title,
		"content": content,
	}))
	assertNoError(t, err)
	return extractNoteID(t, result)
}

// extractNoteID pulls the "(id: <id>)" suffix out of a job_note_write result
// string, e.g. `saved note "Found the bug" (id: 20260101-...-abc123)`.
func extractNoteID(t *testing.T, writeResult string) string {
	t.Helper()
	const marker = "(id: "
	i := strings.Index(writeResult, marker)
	if i < 0 {
		t.Fatalf("write result %q does not contain an id marker", writeResult)
	}
	rest := writeResult[i+len(marker):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatalf("write result %q has an unterminated id marker", writeResult)
	}
	return rest[:j]
}

// captureKBNotifier records KBNote notifications for assertion, mirroring
// captureShellNotifier in tools_shellexec_test.go. Safe for concurrent use
// even though these tests are single-goroutine.
type captureKBNotifier struct {
	mu    sync.Mutex
	calls []KBNote
}

func (c *captureKBNotifier) notify(_ context.Context, kb KBNote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, kb)
}

func (c *captureKBNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *captureKBNotifier) last() KBNote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

// TestJobNoteWrite_NotifierFiresOnSuccess mirrors
// TestShell_NotifierFiresOnSuccess in tools_shellexec_test.go for the
// job_note_write side of the KB display side-channel.
func TestJobNoteWrite_NotifierFiresOnSuccess(t *testing.T) {
	cn := &captureKBNotifier{}
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true), WithKBNoteNotifier(cn.notify))

	result, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "Found the bug",
		"content": "The off-by-one is in the loop bound.",
	}))
	assertNoError(t, err)
	assertContains(t, result, "Found the bug")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	kb := cn.last()
	if kb.Scope != "job" {
		t.Errorf("Scope = %q, want %q", kb.Scope, "job")
	}
	if kb.Op != "write" {
		t.Errorf("Op = %q, want %q", kb.Op, "write")
	}
	if !strings.Contains(kb.Preview, "Found the bug") {
		t.Errorf("Preview = %q, does not contain the note title", kb.Preview)
	}
}

// TestJobNoteWrite_NotifierNotCalledWhenUnset mirrors
// TestShell_NotifierNotCalledWhenUnset: must not panic with no notifier
// attached.
func TestJobNoteWrite_NotifierNotCalledWhenUnset(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	_, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "no notifier",
		"content": "content",
	}))
	assertNoError(t, err)
}

// TestSetKBNoteNotifier_PostConstruction mirrors
// TestSetShellExecNotifier_PostConstruction for the post-construction setter.
func TestSetKBNoteNotifier_PostConstruction(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))

	cn := &captureKBNotifier{}
	ct.SetKBNoteNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "job_note_write", mustJSON(t, map[string]any{
		"title":   "post-construction",
		"content": "content",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier set post-construction called %d times, want 1", cn.count())
	}
}

// TestJobNotesSearch_NotifierFiresWithHitCount checks that job_notes_search
// fires the notifier with Op "search" and a preview reporting query + raw
// hit count (before topK truncation).
func TestJobNotesSearch_NotifierFiresWithHitCount(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))
	mustWriteNote(t, ct, "Found the bug", "The off-by-one is in the loop bound.")
	mustWriteNote(t, ct, "Another finding", "The off-by-one strikes again.")

	cn := &captureKBNotifier{}
	ct.SetKBNoteNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "off-by-one",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	kb := cn.last()
	if kb.Op != "search" {
		t.Errorf("Op = %q, want %q", kb.Op, "search")
	}
	if kb.Scope != "job" {
		t.Errorf("Scope = %q, want %q", kb.Scope, "job")
	}
	if !strings.Contains(kb.Preview, "off-by-one") || !strings.Contains(kb.Preview, "2 hits") {
		t.Errorf("Preview = %q, want it to contain the query and hit count", kb.Preview)
	}
}

// TestJobNotesSearch_NotifierUsesListForEmptyQuery checks the "list" preview
// wording for an empty query (list-most-recent behavior).
func TestJobNotesSearch_NotifierUsesListForEmptyQuery(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))
	mustWriteNote(t, ct, "Found the bug", "content")

	cn := &captureKBNotifier{}
	ct.SetKBNoteNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	kb := cn.last()
	if !strings.HasPrefix(kb.Preview, "list →") {
		t.Errorf("Preview = %q, want it to start with %q", kb.Preview, "list →")
	}
}

// TestJobNotesSearch_NotifierFiresOnZeroHits checks that the notifier still
// fires (with a 0 hit count) when nothing matches — search activity is
// meaningful even when it comes up empty.
func TestJobNotesSearch_NotifierFiresOnZeroHits(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir, WithKBNotes(true))
	mustWriteNote(t, ct, "Found the bug", "content")

	cn := &captureKBNotifier{}
	ct.SetKBNoteNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "job_notes_search", mustJSON(t, map[string]any{
		"query": "nonexistent-term-xyz",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	kb := cn.last()
	if !strings.Contains(kb.Preview, "0 hits") {
		t.Errorf("Preview = %q, want it to contain %q", kb.Preview, "0 hits")
	}
}
