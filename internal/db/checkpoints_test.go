package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jefflinse/rhizome"
)

func TestCheckpointStore_SaveLoadDelete(t *testing.T) {
	store := openTestStore(t)
	cp := store.CheckpointStore()
	ctx := context.Background()

	// Load with no checkpoint → ErrNoCheckpoint.
	if _, _, err := cp.Load(ctx, "thread-1"); !errors.Is(err, rhizome.ErrNoCheckpoint) {
		t.Fatalf("Load(missing) error = %v, want ErrNoCheckpoint", err)
	}

	// Save then Load round-trips node name and data.
	if err := cp.Save(ctx, "thread-1", "node-a", []byte("state-a")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	node, data, err := cp.Load(ctx, "thread-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if node != "node-a" || string(data) != "state-a" {
		t.Fatalf("Load = (%q, %q), want (node-a, state-a)", node, data)
	}

	// Save again overwrites (rhizome keeps only the latest per thread).
	if err := cp.Save(ctx, "thread-1", "node-b", []byte("state-b")); err != nil {
		t.Fatalf("Save (overwrite): %v", err)
	}
	node, data, err = cp.Load(ctx, "thread-1")
	if err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
	if node != "node-b" || string(data) != "state-b" {
		t.Fatalf("Load after overwrite = (%q, %q), want (node-b, state-b)", node, data)
	}

	// Threads are independent.
	if _, _, err := cp.Load(ctx, "thread-2"); !errors.Is(err, rhizome.ErrNoCheckpoint) {
		t.Fatalf("Load(other thread) error = %v, want ErrNoCheckpoint", err)
	}

	// Delete removes the checkpoint; a second delete is a no-op.
	if err := cp.Delete(ctx, "thread-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := cp.Load(ctx, "thread-1"); !errors.Is(err, rhizome.ErrNoCheckpoint) {
		t.Fatalf("Load after delete error = %v, want ErrNoCheckpoint", err)
	}
	if err := cp.Delete(ctx, "thread-1"); err != nil {
		t.Fatalf("Delete (no-op): %v", err)
	}
}
