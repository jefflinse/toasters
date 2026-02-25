package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/progress"
)

var mcpServerCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "Start the Toasters MCP server for agent progress reporting",
	Long:  "Starts an MCP server on stdin/stdout that exposes progress reporting tools to external agents (e.g., Claude CLI subprocesses).",
	RunE:  runMCPServer,
}

var mcpServerDBPath string

func init() {
	mcpServerCmd.Flags().StringVar(&mcpServerDBPath, "db", "", "Path to the SQLite database (overrides config)")
	rootCmd.AddCommand(mcpServerCmd)
}

func runMCPServer(cmd *cobra.Command, _ []string) error {
	// Resolve database path.
	dbPath := mcpServerDBPath
	if dbPath == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		dbPath, err = config.DatabasePath(cfg)
		if err != nil {
			return fmt.Errorf("resolving database path: %w", err)
		}
	}

	// Open database.
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Handle signals for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start MCP server on stdio.
	return progress.StartStdioServer(ctx, store)
}
