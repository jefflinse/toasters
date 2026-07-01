package runtime

import (
	"context"
	"sync"
	"testing"
)

// captureNotifier records FileChange notifications for assertion. Safe for
// concurrent use even though these tests are single-goroutine.
type captureNotifier struct {
	mu    sync.Mutex
	calls []FileChange
}

func (c *captureNotifier) notify(_ context.Context, fc FileChange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, fc)
}

func (c *captureNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *captureNotifier) last() FileChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

func TestWriteFile_NotifierFiresOnCreate(t *testing.T) {
	dir := t.TempDir()
	cn := &captureNotifier{}
	ct := NewCoreTools(dir, WithFileChangeNotifier(cn.notify))

	result, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path":    "new.txt",
		"content": "hello\nworld\n",
	}))
	assertNoError(t, err)
	assertContains(t, result, "wrote") // returned result string must not carry the diff
	assertNotContains(t, result, "@@")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	fc := cn.last()
	if fc.ToolName != "write_file" {
		t.Errorf("ToolName = %q, want write_file", fc.ToolName)
	}
	if fc.Path != "new.txt" {
		t.Errorf("Path = %q, want new.txt (model-passed, not resolved)", fc.Path)
	}
	if !fc.Created {
		t.Errorf("Created = false, want true")
	}
	if fc.Added != 2 {
		t.Errorf("Added = %d, want 2", fc.Added)
	}
}

func TestWriteFile_NotifierSkippedOnNoOp(t *testing.T) {
	dir := t.TempDir()
	cn := &captureNotifier{}
	ct := NewCoreTools(dir, WithFileChangeNotifier(cn.notify))

	writeTestFile(t, dir, "same.txt", "unchanged\n")
	_, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path":    "same.txt",
		"content": "unchanged\n",
	}))
	assertNoError(t, err)

	if cn.count() != 0 {
		t.Errorf("notifier called %d times for a no-op write, want 0", cn.count())
	}
}

func TestWriteFile_NotifierSkippedOnError(t *testing.T) {
	dir := t.TempDir()
	cn := &captureNotifier{}
	ct := NewCoreTools(dir, WithFileChangeNotifier(cn.notify))

	const maxWriteContentSize = 50 * 1024 * 1024
	oversized := make([]byte, maxWriteContentSize+1)
	_, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path":    "toobig.txt",
		"content": string(oversized),
	}))
	assertError(t, err)

	if cn.count() != 0 {
		t.Errorf("notifier called %d times on a failed write, want 0", cn.count())
	}
}

func TestEditFile_NotifierFiresOnModify(t *testing.T) {
	dir := t.TempDir()
	cn := &captureNotifier{}
	ct := NewCoreTools(dir, WithFileChangeNotifier(cn.notify))

	writeTestFile(t, dir, "edit.txt", "hello world\nfoo bar\nbaz qux\n")
	result, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
		"path":       "edit.txt",
		"old_string": "foo bar",
		"new_string": "FOO BAR",
	}))
	assertNoError(t, err)
	assertContains(t, result, "edited")
	assertNotContains(t, result, "@@") // diff must never leak into the LLM-visible result

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	fc := cn.last()
	if fc.ToolName != "edit_file" {
		t.Errorf("ToolName = %q, want edit_file", fc.ToolName)
	}
	if fc.Path != "edit.txt" {
		t.Errorf("Path = %q, want edit.txt", fc.Path)
	}
	if fc.Created {
		t.Errorf("Created = true, want false")
	}
	if fc.Added != 1 || fc.Removed != 1 {
		t.Errorf("Added=%d Removed=%d, want 1/1", fc.Added, fc.Removed)
	}
}

func TestEditFile_NotifierSkippedOnErrorPaths(t *testing.T) {
	dir := t.TempDir()
	cn := &captureNotifier{}
	ct := NewCoreTools(dir, WithFileChangeNotifier(cn.notify))

	writeTestFile(t, dir, "edit3.txt", "aaa\naaa\naaa\n")
	_, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
		"path":       "edit3.txt",
		"old_string": "aaa",
		"new_string": "bbb",
	}))
	assertError(t, err)

	_, err = ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
		"path":       "nonexistent.txt",
		"old_string": "foo",
		"new_string": "bar",
	}))
	assertError(t, err)

	if cn.count() != 0 {
		t.Errorf("notifier called %d times across failed edits, want 0", cn.count())
	}
}

func TestSetFileChangeNotifier_PostConstruction(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	cn := &captureNotifier{}
	ct.SetFileChangeNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
		"path":    "post.txt",
		"content": "content\n",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier set post-construction called %d times, want 1", cn.count())
	}
}
