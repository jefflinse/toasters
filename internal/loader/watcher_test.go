package loader

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// waitForChan waits for a signal on ch or times out.
func waitForChan(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func TestWatcher_FileChange(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()

	// Set up a user skill file.
	writeFile(t, filepath.Join(configDir, "user", "skills", "dev.md"), goDevSkillMD)

	l := New(store, configDir)

	changed := make(chan struct{}, 1)
	w, err := NewWatcher(l, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = w.Start(ctx) }()

	// Give the watcher time to set up watches.
	time.Sleep(50 * time.Millisecond)

	// Modify the .md file.
	writeFile(t, filepath.Join(configDir, "user", "skills", "dev.md"), `---
name: Updated Dev Skill
description: Updated description
---
Updated prompt.
`)

	if !waitForChan(changed, 2*time.Second) {
		t.Fatal("onChange was not called after .md file change")
	}

	cancel()
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWatcher_Debounce(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()

	writeFile(t, filepath.Join(configDir, "user", "skills", "dev.md"), goDevSkillMD)

	l := New(store, configDir)

	var callCount atomic.Int32
	changed := make(chan struct{}, 10)
	w, err := NewWatcher(l, func() {
		callCount.Add(1)
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = w.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Rapid-fire 5 changes within the debounce window.
	skillPath := filepath.Join(configDir, "user", "skills", "dev.md")
	for i := range 5 {
		writeFile(t, skillPath, goDevSkillMD+"\n"+string(rune('a'+i)))
		time.Sleep(20 * time.Millisecond) // well within 200ms debounce
	}

	// Wait for the debounced callback.
	if !waitForChan(changed, 2*time.Second) {
		t.Fatal("onChange was not called")
	}

	// Wait a bit more to ensure no extra callbacks fire.
	time.Sleep(500 * time.Millisecond)

	count := callCount.Load()
	if count != 1 {
		t.Errorf("expected 1 onChange call (debounced), got %d", count)
	}

	cancel()
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWatcher_IgnoresNonMD(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()

	// Create the user/skills directory so the watcher can watch it.
	skillsDir := filepath.Join(configDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("creating skills dir: %v", err)
	}

	l := New(store, configDir)

	changed := make(chan struct{}, 1)
	w, err := NewWatcher(l, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = w.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Write a .txt file — should be ignored.
	writeFile(t, filepath.Join(skillsDir, "notes.txt"), "not a definition file")

	// Write a .yaml file — should also be ignored.
	writeFile(t, filepath.Join(skillsDir, "config.yaml"), "key: value")

	// Wait long enough for a debounce cycle to pass.
	if waitForChan(changed, 500*time.Millisecond) {
		t.Fatal("onChange was called for non-.md file change")
	}

	cancel()
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWatcher_StopCleanup(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()

	writeFile(t, filepath.Join(configDir, "user", "skills", "dev.md"), goDevSkillMD)

	l := New(store, configDir)

	w, err := NewWatcher(l, func() {})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = w.Start(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel context and stop watcher.
	cancel()
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify the Start goroutine exits.
	if !waitForChan(done, 2*time.Second) {
		t.Fatal("Start goroutine did not exit after Stop")
	}
}
