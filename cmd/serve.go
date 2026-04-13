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
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/modelsdev"
	"github.com/jefflinse/toasters/internal/prompt"
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
	bootstrap.UserFS = defaults.UserFiles
	if err := bootstrap.Run(configDir, defaults.SystemFiles, defaults.DefaultConfig); err != nil {
		slog.Warn("bootstrap failed", "error", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Set up logging to a SERVER-only file so we don't race the client logger
	// on the same file. Truncate on startup so each server run has a clean log
	// to inspect.
	if err := os.MkdirAll(configDir, 0755); err == nil {
		logPath := filepath.Join(configDir, "server.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
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

	// Create prompt engine for role-based prompt composition.
	// Must be created before the loader so role-based teams can resolve.
	// System roles load first so user definitions can override them.
	promptEngine := prompt.NewEngine()
	systemPromptDir := filepath.Join(configDir, "system")
	if err := promptEngine.LoadDir(systemPromptDir, "system"); err != nil {
		slog.Warn("failed to load system prompt definitions", "dir", systemPromptDir, "error", err)
	}
	userDir := filepath.Join(configDir, "user")
	if err := promptEngine.LoadDir(userDir, "user"); err != nil {
		slog.Warn("failed to load user prompt definitions", "dir", userDir, "error", err)
	}
	slog.Info("loaded prompt definitions", "roles", len(promptEngine.Roles()))

	// Load definitions from files into DB.
	var ldr *loader.Loader
	if store != nil {
		ldr = loader.New(store, configDir)
		ldr.SetPromptEngine(promptEngine)
		if err := ldr.Load(context.Background()); err != nil {
			slog.Warn("initial definition load failed", "error", err)
		}
	}

	// Resolve default provider/model for agent sessions.
	// Fall back to the operator's provider/model when agents.defaults is empty.
	defaultProvider := cfg.Agents.Defaults.Provider
	if defaultProvider == "" {
		defaultProvider = cfg.Operator.Provider
	}
	defaultModel := cfg.Agents.Defaults.Model
	if defaultModel == "" {
		defaultModel = cfg.Operator.Model
	}

	// Create provider registry and register providers from providers/*.yaml.
	registry := provider.NewRegistry()
	registerProviders(registry, ldr)

	// Create the runtime for agent session management.
	rt := runtime.New(store, registry)
	rt.SetPromptEngine(promptEngine)
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

	// Look up the operator's provider config to capture the endpoint URL for
	// the sidebar. Falls back to empty string if not found, which is fine —
	// the sidebar will simply leave the field blank.
	var operatorEndpoint string
	if ldr != nil {
		for _, pc := range ldr.Providers() {
			if pc.Key() == cfg.Operator.Provider {
				operatorEndpoint = pc.Endpoint
				break
			}
		}
	}

	// Compose the operator agent's system prompt via the prompt engine.
	var operatorPrompt string
	if composed, err := promptEngine.Compose("operator", nil); err != nil {
		slog.Warn("failed to compose operator prompt", "error", err)
	} else {
		operatorPrompt = composed
	}

	// Initialize the models.dev catalog client for the provider/model browser.
	catalog := modelsdev.NewCatalogSource(modelsdev.NewClient())

	// Create the LocalService.
	svc := service.NewLocal(service.LocalConfig{
		Store:            store,
		Runtime:          rt,
		MCPManager:       mcpManager,
		Provider:         client,
		Loader:           ldr,
		ConfigDir:        configDir,
		WorkspaceDir:     workspaceDir,
		TeamsDir:         teamsDir,
		OperatorModel:    cfg.Operator.Model,
		OperatorEndpoint: operatorEndpoint,
		StartTime:        time.Now(),
		Catalog:          catalog,
		Registry:         registry,
		PromptEngine:     promptEngine,
		DefaultProvider:  defaultProvider,
		DefaultModel:     defaultModel,
	})
	defer svc.Shutdown()

	// Wire the runtime's session-started callback to broadcast session events
	// through the service event stream. This is the only path by which agent
	// session activity reaches subscribers (TUI, SSE clients).
	rt.OnSessionStarted = svc.BroadcastSessionStarted

	// Create and start the operator event loop.
	// Both a store and a provider are required — if the provider couldn't be
	// resolved (no providers configured, or operator.provider not found), the
	// operator is left nil and the TUI will show the disabled state.
	var op *operator.Operator
	if store != nil && client != nil {
		textFlush := func(text string) {
			svc.BroadcastOperatorText(text, "")
		}
		batcher := newTextBatcher(16*time.Millisecond, textFlush)

		var opErr error
		op, opErr = operator.New(operator.Config{
			Runtime:                rt,
			Provider:               client,
			Model:                  cfg.Operator.Model,
			WorkDir:                workspaceDir,
			Store:                  store,
			Spawner:                rt,
			SystemPrompt:           operatorPrompt,
			SystemEventBroadcaster: svc,
			PromptEngine:           promptEngine,
			DefaultProvider:        defaultProvider,
			DefaultModel:           defaultModel,
			OnText: func(text string) {
				batcher.Add(text)
			},
			OnEvent: func(event operator.Event) {
				svc.BroadcastOperatorEvent(event)
			},
			OnTurnDone: func(tokensIn, tokensOut, reasoningTokens int) {
				batcher.Flush()
				svc.BroadcastOperatorDone(cfg.Operator.Model, tokensIn, tokensOut, reasoningTokens)
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
			registerProviders(registry, ldr)
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

// registerProviders reads provider configs from the loader and registers them
// in the provider registry. Safe to call multiple times (hot-reload).
func registerProviders(registry *provider.Registry, ldr *loader.Loader) {
	if ldr == nil {
		return
	}
	for _, pc := range ldr.Providers() {
		// Expand ${ENV_VAR} references in API key and endpoint.
		pc.APIKey = os.Expand(pc.APIKey, os.Getenv)
		pc.Endpoint = os.Expand(pc.Endpoint, os.Getenv)

		p, err := provider.NewFromConfig(pc)
		if err != nil {
			slog.Warn("failed to create provider", "id", pc.ID, "name", pc.Name, "error", err)
			continue
		}
		registry.Register(pc.Key(), p)
	}
}
