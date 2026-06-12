package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Migration 012 backfills description from summary for PENDING tasks only —
// those haven't run, so summary still holds the original coarse-decompose
// description rather than a status or failure message.
func TestMigration012_BackfillsPendingDescriptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	rawDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer rawDB.Close() //nolint:errcheck
	rawDB.SetMaxOpenConns(1)

	// Build a pre-012 database: schema_version table + migrations 1..11.
	if _, err := rawDB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("creating schema_version: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version >= 12 {
			continue
		}
		if err := applyMigration(rawDB, m); err != nil {
			t.Fatalf("applying migration %03d: %v", m.version, err)
		}
	}

	// Seed old-style rows where summary holds the description.
	if _, err := rawDB.Exec(`INSERT INTO jobs (id, title, status) VALUES ('j1', 'J', 'active')`); err != nil {
		t.Fatalf("seeding job: %v", err)
	}
	seed := func(id, status, summary string) {
		t.Helper()
		if _, err := rawDB.Exec(
			`INSERT INTO tasks (id, job_id, title, status, summary) VALUES (?, 'j1', ?, ?, ?)`,
			id, id, status, summary); err != nil {
			t.Fatalf("seeding task %s: %v", id, err)
		}
	}
	seed("pending-1", "pending", "build the docker image for module X")
	seed("failed-1", "failed", "Interrupted: the server stopped")
	seed("done-1", "completed", "all tests pass")

	// Run the remaining migrations (012+).
	if err := migrate(rawDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var desc string
	if err := rawDB.QueryRow(`SELECT description FROM tasks WHERE id = 'pending-1'`).Scan(&desc); err != nil {
		t.Fatalf("querying pending-1: %v", err)
	}
	if desc != "build the docker image for module X" {
		t.Errorf("pending task description = %q, want backfilled from summary", desc)
	}
	for _, id := range []string{"failed-1", "done-1"} {
		if err := rawDB.QueryRow(`SELECT description FROM tasks WHERE id = ?`, id).Scan(&desc); err != nil {
			t.Fatalf("querying %s: %v", id, err)
		}
		if desc != "" {
			t.Errorf("%s description = %q, want empty (summary holds status text, not a description)", id, desc)
		}
	}
}
