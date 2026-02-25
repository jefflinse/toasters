package db

import (
	"cmp"
	"database/sql"
	"embed"
	"fmt"
	"slices"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrate applies any unapplied migrations to the database.
// It creates the schema_version table if it doesn't exist, then runs
// each migration file whose version number exceeds the current version.
func migrate(db *sql.DB) error {
	// Ensure the schema_version table exists.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version    INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return fmt.Errorf("creating schema_version table: %w", err)
	}

	currentVersion, err := currentSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("loading migrations: %w", err)
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("applying migration %03d: %w", m.version, err)
		}
	}

	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// currentSchemaVersion returns the highest applied migration version, or 0.
func currentSchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// loadMigrations reads all .sql files from the embedded migrations directory,
// parses their version numbers from the filename prefix, and returns them sorted.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations directory: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%03d_", &version); err != nil {
			return nil, fmt.Errorf("parsing version from %q: %w", entry.Name(), err)
		}

		content, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", entry.Name(), err)
		}

		migrations = append(migrations, migration{
			version: version,
			name:    entry.Name(),
			sql:     string(content),
		})
	}

	slices.SortFunc(migrations, func(a, b migration) int {
		return cmp.Compare(a.version, b.version)
	})

	return migrations, nil
}

// applyMigration runs a single migration inside a transaction and records it.
func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("executing SQL: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO schema_version (version) VALUES (?)", m.version,
	); err != nil {
		return fmt.Errorf("recording version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}
