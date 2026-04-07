package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/auth"
	"github.com/jefflinse/toasters/internal/bootstrap"
	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

var (
	serveAddr   string
	serveNoAuth bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the toasters server",
	Long: `Start the toasters HTTP server for remote TUI connections.

The server exposes a REST API and SSE event stream that remote TUI clients
can connect to. By default, authentication is enabled using a bearer token
stored in ~/.config/toasters/server.token.

Examples:
  toasters serve                    # Start server on :8080
  toasters serve --addr :3000       # Start server on port 3000
  toasters serve --no-auth          # Start server without authentication`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080", "address to listen on")
	serveCmd.Flags().BoolVar(&serveNoAuth, "no-auth", false, "disable authentication")
}

func runServe(cmd *cobra.Command, args []string) error {
	config.BindFlags(cmd)

	configDir, err := config.Dir()
	if err != nil {
		return err
	}

	// Bootstrap runs before config.Load() so that the default config.yaml is
	// written to disk before Viper reads it.
	if err := bootstrap.Run(configDir, defaults.SystemFiles, defaults.DefaultConfig); err != nil {
		slog.Warn("bootstrap failed", "error", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Set up logging to file.
	if err := os.MkdirAll(configDir, 0755); err == nil {
		logPath := filepath.Join(configDir, "toasters.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
			defer func() { _ = f.Close() }()
		} else {
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	workspaceDir, err := config.WorkspaceDir(cfg)
	if err != nil {
		return err
	}

	teamsDir := cfg.Operator.TeamsDir

	// Open SQLite database for persistence.
	var store db.Store
	dbPath, err := config.DatabasePath(cfg, workspaceDir)
	if err != nil {
		slog.Warn("failed to resolve database path", "error", err)
	} else {
		sqliteStore, dbErr := db.Open(dbPath)
		if dbErr != nil {
			slog.Warn("failed to open database", "path", dbPath, "error", dbErr)
		} else {
			store = sqliteStore
			defer func() { _ = sqliteStore.Close() }()
		}
	}

	// Load definitions from files into DB.
	var ldr *loader.Loader
	if store != nil {
		ldr = loader.New(store, configDir)
		if err := ldr.Load(context.Background()); err != nil {
			slog.Warn("initial definition load failed", "error", err)
		}
	}

	// Create composer for runtime agent composition.
	var composer *compose.Composer
	if store != nil {
		composer = compose.New(store, cfg.Agents.Defaults.Provider, cfg.Agents.Defaults.Model)
	}

	// Create provider registry and register configured providers.
	registry := provider.NewRegistry()
	for _, pc := range cfg.Providers {
		p, provErr := provider.NewFromConfig(pc)
		if provErr != nil {
			slog.Warn("failed to create provider", "id", pc.ID, "name", pc.Name, "error", provErr)
			continue
		}
		registry.Register(pc.Key(), p)
	}

	// Create the runtime for agent session management.
	rt := runtime.New(store, registry)
	defer rt.Shutdown()

	// Initialize MCP manager and connect to configured servers.
	mcpManager := mcp.NewManager()
	if len(cfg.MCP.Servers) > 0 {
		if err := mcpManager.Connect(context.Background(), cfg.MCP.Servers); err != nil {
			slog.Warn("MCP connect error", "error", err)
		}
	}
	defer func() { _ = mcpManager.Close() }()

	// Wire MCP tools into agent runtime with result truncation.
	if len(mcpManager.Tools()) > 0 {
		truncatingCaller := mcp.NewTruncatingCaller(mcpManager, mcp.DefaultMaxResultLen)
		rt.SetMCPCaller(truncatingCaller, mcp.ToRuntimeToolDefs(mcpManager.Tools()))
	}

	client, err := resolveOperatorProvider(cfg, registry)
	if err != nil {
		return err
	}

	// Compose the operator agent from its .md file definition.
	var operatorPrompt string
	if composer != nil {
		composedOperator, composeErr := composer.Compose(context.Background(), "operator", "system")
		if composeErr != nil {
			slog.Warn("failed to compose operator agent, using empty prompt", "error", composeErr)
		} else {
			operatorPrompt = composedOperator.SystemPrompt
		}
	}

	// Create the LocalService.
	svc := service.NewLocal(service.LocalConfig{
		Store:         store,
		Runtime:       rt,
		MCPManager:    mcpManager,
		Provider:      client,
		Composer:      composer,
		Loader:        ldr,
		ConfigDir:     configDir,
		WorkspaceDir:  workspaceDir,
		TeamsDir:      teamsDir,
		OperatorModel: cfg.Operator.Model,
		StartTime:     time.Now(),
	})
	defer svc.Shutdown()

	// Wire the runtime's session-started callback to broadcast session events
	// through the service event stream. This is the only path by which agent
	// session activity reaches subscribers (TUI, SSE clients).
	rt.OnSessionStarted = svc.BroadcastSessionStarted

	// Create and start the operator event loop.
	var op *operator.Operator
	if store != nil {
		textFlush := func(text string) {
			svc.BroadcastOperatorText(text, "")
		}
		batcher := newTextBatcher(16*time.Millisecond, textFlush)

		var opErr error
		op, opErr = operator.New(operator.Config{
			Runtime:      rt,
			Provider:     client,
			Model:        cfg.Operator.Model,
			WorkDir:      workspaceDir,
			Store:        store,
			Composer:     composer,
			Spawner:      rt,
			SystemPrompt: operatorPrompt,
			OnText: func(text string) {
				batcher.Add(text)
			},
			OnEvent: func(event operator.Event) {
				svc.BroadcastOperatorEvent(event)
			},
			OnTurnDone: func() {
				batcher.Flush()
				svc.BroadcastOperatorDone(cfg.Operator.Model, 0, 0, 0)
			},
		})
		if opErr != nil {
			slog.Warn("failed to create operator", "error", opErr)
		}
	}

	// Wire the operator into the service.
	if op != nil {
		svc.SetOperator(op)
	}

	// Handle authentication token.
	var token string
	if !serveNoAuth {
		token, err = auth.EnsureToken(configDir)
		if err != nil {
			return fmt.Errorf("ensuring server token: %w", err)
		}
		tokenPath := filepath.Join(configDir, "server.token")
		fmt.Fprintf(os.Stderr, "Auth token: %s\n", tokenPath)
	}

	// Create the HTTP server.
	srv := server.New(svc, server.WithToken(token))

	// Start the operator if it was created.
	if op != nil {
		opCtx, opCancel := context.WithCancel(context.Background())
		defer opCancel()
		op.Start(opCtx)
	}

	// Start watching for definition file changes.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	if ldr != nil {
		defWatcher, defWatchErr := loader.NewWatcher(ldr, func() {
			svc.BroadcastDefinitionsReloaded()
		})
		if defWatchErr != nil {
			slog.Warn("failed to create definitions watcher", "error", defWatchErr)
		} else {
			go func() {
				if err := defWatcher.Start(watchCtx); err != nil && watchCtx.Err() == nil {
					slog.Error("definitions watcher error", "error", err)
				}
			}()
			defer func() { _ = defWatcher.Stop() }()
		}
	}

	// Start the HTTP server.
	if err := srv.Start(serveAddr); err != nil {
		return fmt.Errorf("starting server: %w", err)
	}

	// Print startup info.
	fmt.Fprintf(os.Stderr, "Toasters server listening on %s\n", srv.Addr())

	// Set up signal handling for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Track shutdown state to prevent double shutdown.
	var shuttingDown atomic.Bool

	// Wait for shutdown signal.
	<-sigChan
	if shuttingDown.CompareAndSwap(false, true) {
		fmt.Fprintf(os.Stderr, "\nShutting down...\n")

		// Force-close all SSE connections first to unblock writes.
		srv.CloseAllSSEConnections()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown error", "error", err)
			return err
		}
		fmt.Fprintf(os.Stderr, "Server stopped\n")
	}

	return nil
}
