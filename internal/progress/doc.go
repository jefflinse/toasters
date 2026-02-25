// Package progress provides protocol-agnostic handlers for agent progress
// reporting. Handlers write to SQLite via the db.Store interface and are
// used by both in-process agents (direct function calls) and external agents
// (via the MCP server in server.go).
package progress
