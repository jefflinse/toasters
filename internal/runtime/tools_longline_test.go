package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lockfiles and minified JS routinely exceed bufio.Scanner's 64KB default;
// read_file and grep must handle them instead of erroring with "token too
// long", which made such files unreadable for workers.
func TestReadFile_LongLine(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("a", 200*1024)
	if err := os.WriteFile(filepath.Join(dir, "minified.js"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ct := NewCoreTools(dir)
	out, err := ct.readFile(context.Background(), json.RawMessage(`{"path":"minified.js"}`))
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if !strings.Contains(out, long[:100]) {
		t.Error("long line content missing from read_file output")
	}
}

func TestGrep_LongLine(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("b", 200*1024) + "needle"
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ct := NewCoreTools(dir)
	out, err := ct.grepFiles(context.Background(), json.RawMessage(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if strings.Contains(out, "(no matches)") {
		t.Error("grep failed to match inside a long line")
	}
}
