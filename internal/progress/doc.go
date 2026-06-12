// Package progress provides protocol-agnostic handlers for worker progress
// reporting. Handlers write to SQLite via the db.Store interface and are
// used by both in-process workers (direct function calls) and external workers
// (via the MCP server in server.go).
package progress
