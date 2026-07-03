package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// captureShellNotifier records ShellExec notifications for assertion. Safe
// for concurrent use even though these tests are single-goroutine.
type captureShellNotifier struct {
	mu    sync.Mutex
	calls []ShellExec
}

func (c *captureShellNotifier) notify(_ context.Context, se ShellExec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, se)
}

func (c *captureShellNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *captureShellNotifier) last() ShellExec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

func TestShell_NotifierFiresOnSuccess(t *testing.T) {
	cn := &captureShellNotifier{}
	ct := NewCoreTools(t.TempDir(), WithShell(true), WithShellExecNotifier(cn.notify))

	result, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "echo hello",
	}))
	assertNoError(t, err)
	assertContains(t, result, "hello")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	se := cn.last()
	if se.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", se.ExitCode)
	}
	if se.TimedOut {
		t.Errorf("TimedOut = true, want false")
	}
	if se.Truncated {
		t.Errorf("Truncated = true, want false for a short command")
	}
	if se.OutputBytes != len(result) {
		t.Errorf("OutputBytes = %d, want %d (raw combined output size)", se.OutputBytes, len(result))
	}
	if se.Command != "echo hello" {
		t.Errorf("Command = %q, want %q", se.Command, "echo hello")
	}
	if se.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", se.DurationMs)
	}
}

func TestShell_NotifierFiresOnNonzeroExit(t *testing.T) {
	cn := &captureShellNotifier{}
	ct := NewCoreTools(t.TempDir(), WithShell(true), WithShellExecNotifier(cn.notify))

	result, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "echo output && exit 2",
	}))
	// Non-zero exit is not an error at the CoreTools level — the LLM must
	// still see the output — but the notifier must report the real exit code.
	assertNoError(t, err)
	assertContains(t, result, "output")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	se := cn.last()
	if se.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", se.ExitCode)
	}
	if se.TimedOut {
		t.Errorf("TimedOut = true, want false")
	}
}

func TestShell_NotifierFiresOnTimeout(t *testing.T) {
	cn := &captureShellNotifier{}
	ct := NewCoreTools(t.TempDir(), WithShell(true), WithShellExecNotifier(cn.notify))

	_, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "sleep 10",
		"timeout": 1,
	}))
	assertError(t, err)
	assertContains(t, err.Error(), "timed out")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	se := cn.last()
	if !se.TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	// A killed command's exit code isn't meaningfully "0" or any particular
	// signal-derived value across platforms — just confirm it's not left at
	// the zero-valued Go default of 0, which would misreport a timeout as a
	// clean exit.
	if se.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want a non-clean-exit sentinel for a killed process")
	}
}

func TestShell_NotifierTruncatedFlagTracksResultCap(t *testing.T) {
	cn := &captureShellNotifier{}
	ct := NewCoreTools(t.TempDir(), WithShell(true), WithShellExecNotifier(cn.notify))

	// Comfortably over maxToolResultBytes (8KB) using a portable /bin/sh
	// pipeline — no bash-isms.
	result, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "head -c 9000 /dev/zero | tr '\\0' 'x'",
	}))
	assertNoError(t, err)
	if len(result) <= maxToolResultBytes {
		t.Fatalf("test setup broken: result is %d bytes, want > %d", len(result), maxToolResultBytes)
	}

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	se := cn.last()
	if !se.Truncated {
		t.Errorf("Truncated = false, want true for a %d-byte result", len(result))
	}
	if se.OutputBytes != len(result) {
		t.Errorf("OutputBytes = %d, want %d (uncapped raw size)", se.OutputBytes, len(result))
	}
}

func TestShell_NotifierNotCalledWhenUnset(t *testing.T) {
	ct := NewCoreTools(t.TempDir(), WithShell(true))

	// Must not panic with no notifier attached.
	_, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "echo hello",
	}))
	assertNoError(t, err)
}

func TestSetShellExecNotifier_PostConstruction(t *testing.T) {
	ct := NewCoreTools(t.TempDir(), WithShell(true))

	cn := &captureShellNotifier{}
	ct.SetShellExecNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
		"command": "echo hi",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier set post-construction called %d times, want 1", cn.count())
	}
}

func TestDisplayCommand_CapsLongCommand(t *testing.T) {
	short := "echo hi"
	if got := displayCommand(short); got != short {
		t.Errorf("displayCommand(%q) = %q, want unchanged", short, got)
	}

	long := strings.Repeat("x", maxShellExecCommandBytes+50)
	got := displayCommand(long)
	if len(got) >= len(long) {
		t.Errorf("displayCommand did not shorten a %d-byte command: got %d bytes", len(long), len(got))
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("displayCommand(long) = %q, want a truncation marker suffix", got)
	}
}
