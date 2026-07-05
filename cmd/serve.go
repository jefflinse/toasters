package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/auth"
	"github.com/jefflinse/toasters/internal/bootstrap"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/contextwindow"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/modelsdev"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/prompt"
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

The server binds to loopback by default. Binding to a non-loopback address
exposes the API (which can run shell commands via workers) to the network
over unencrypted HTTP, and is refused entirely when combined with --no-auth.

Examples:
  toasters serve                          # Start server on 127.0.0.1:8421
  toasters serve --addr 127.0.0.1:3000    # Start server on port 3000
  toasters serve --addr 0.0.0.0:8421      # Expose to the network (use with care)
  toasters serve --no-auth                # Start server without authentication`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:8421", "address to listen on")
	serveCmd.Flags().BoolVar(&serveNoAuth, "no-auth", false, "disable authentication")
}

// isLoopbackAddr reports whether addr binds only to a loopback interface.
// An empty host (":8421") binds all interfaces and is not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func runServe(cmd *cobra.Command, args []string) error {
	// Past flag parsing: errors below are runtime failures (bind failure,
	// bootstrap error, etc.), not misuse — don't dump usage on top of them.
	cmd.SilenceUsage = true

	config.BindFlags(cmd)

	if !isLoopbackAddr(serveAddr) {
		if serveNoAuth {
			return fmt.Errorf("refusing to listen on non-loopback address %q with --no-auth: anyone on the network could run shell commands through the API; bind to 127.0.0.1 or remove --no-auth", serveAddr)
		}
		fmt.Fprintf(os.Stderr, "WARNING: listening on non-loopback address %s — the API is reachable from the network over unencrypted HTTP (the bearer token is sniffable) and workers can run shell commands. Only do this on a trusted network.\n", serveAddr)
	}

	configDir, err := config.Dir()
	if err != nil {
		return err
	}

	// Bootstrap runs before config.Load() so that the default config.yaml is
	// written to disk before Viper reads it.
	bootstrap.UserFS = defaults.UserFiles
	bootstrap.ProviderFS = defaults.ProviderFiles
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

	// Open SQLite database for persistence.
	var store db.Store
	// checkpoints, when non-nil, backs node-granular graph checkpoint/resume.
	// Only the SQLite store provides it; it stays nil for any other backend.
	var checkpoints *db.CheckpointStore
	dbPath, err := config.DatabasePath(cfg, workspaceDir)
	if err != nil {
		slog.Warn("failed to resolve database path", "error", err)
	} else {
		sqliteStore, dbErr := db.Open(dbPath)
		if dbErr != nil {
			slog.Warn("failed to open database", "path", dbPath, "error", dbErr)
		} else {
			store = sqliteStore
			checkpoints = sqliteStore.CheckpointStore()
			defer func() { _ = sqliteStore.Close() }()

			// Reclaim rows orphaned by a previous run (crash or unclean stop):
			// sessions still 'active' are failed (their runtime is gone) and
			// tasks still 'in_progress' are reset to 'pending' so the operator
			// re-dispatches them once its event loop starts (see
			// Operator.recoverInterrupted). Without this a restart would leave
			// jobs 'active' but stalled forever.
			if sessions, tasks, recErr := sqliteStore.ReconcileInterrupted(context.Background()); recErr != nil {
				slog.Warn("failed to reconcile interrupted work", "error", recErr)
			} else if sessions > 0 || tasks > 0 {
				slog.Info("reclaimed work interrupted by previous shutdown",
					"sessions_failed", sessions, "tasks_requeued", tasks)
			}

			// Blockers still pending in the DB have no waiting caller after a
			// restart — mark them cancelled so history doesn't show phantom
			// open questions.
			if swept, recErr := sqliteStore.SweepUnresolvedBlockers(context.Background()); recErr != nil {
				slog.Warn("failed to sweep unresolved blockers", "error", recErr)
			} else if swept > 0 {
				slog.Info("cancelled blockers orphaned by previous shutdown", "count", swept)
			}
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
	promptEngine.SetGlobal("task.granularity", config.ValidTaskGranularity(cfg.TaskGranularity))
	promptEngine.SetGlobal("available.toolchains", strings.Join(promptEngine.Toolchains(), ", "))
	if err := prompt.ApplyGranularity(promptEngine, "coarse", config.ValidGranularity("coarse", cfg.CoarseGranularity)); err != nil {
		slog.Warn("failed to apply coarse_granularity", "error", err)
	}
	if err := prompt.ApplyGranularity(promptEngine, "fine", config.ValidGranularity("fine", cfg.FineGranularity)); err != nil {
		slog.Warn("failed to apply fine_granularity", "error", err)
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

	// Resolve default provider/model for worker sessions.
	// Fall back to the operator's provider/model when agents.defaults is empty.
	defaultProvider := cfg.Workers.Defaults.Provider
	if defaultProvider == "" {
		defaultProvider = cfg.Operator.Provider
	}
	defaultModel := cfg.Workers.Defaults.Model
	if defaultModel == "" {
		defaultModel = cfg.Operator.Model
	}

	// Create provider registry and register providers from providers/*.yaml.
	registry := provider.NewRegistry()
	registerProviders(registry, ldr)

	// Create the runtime for worker session management.
	rt := runtime.New(store, registry)
	rt.SetPromptEngine(promptEngine)
	rt.SetCompactionThreshold(config.ValidCompactionThreshold(
		cfg.WorkerCompactionThreshold, config.DefaultWorkerCompactionThreshold))
	rt.SetKBEnabled(cfg.KB.Enabled)
	defer rt.Shutdown()

	// Initialize MCP manager and connect to configured servers.
	mcpManager := mcp.NewManager()
	if len(cfg.MCP.Servers) > 0 {
		if err := mcpManager.Connect(context.Background(), cfg.MCP.Servers); err != nil {
			slog.Warn("MCP connect error", "error", err)
		}
	}
	defer func() { _ = mcpManager.Close() }()

	// Wire MCP tools into the worker runtime with result truncation. Also
	// re-wired by the reconnect loop below whenever a failed server comes
	// back, so its tools become available without a restart.
	wireMCPTools := func() {
		if len(mcpManager.Tools()) > 0 {
			truncatingCaller := mcp.NewTruncatingCaller(mcpManager, mcp.DefaultMaxResultLen)
			rt.SetMCPCaller(truncatingCaller, mcp.ToRuntimeToolDefs(mcpManager.Tools()))
		}
	}
	wireMCPTools()

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

	// Compose the operator's system prompt via the prompt engine.
	var operatorPrompt string
	if composed, err := promptEngine.Compose("operator", nil, nil); err != nil {
		slog.Warn("failed to compose operator prompt", "error", err)
	} else {
		operatorPrompt = composed
	}

	// Initialize the models.dev catalog client for the provider/model browser
	// and the context-window resolver (which uses the raw client for lookups).
	mdClient := modelsdev.NewClient()
	catalog := modelsdev.NewCatalogSource(mdClient)

	// Context-window resolver: provider-reported loaded context, then the
	// provider definition's context_window override, then the models.dev
	// catalog. Shared by DTO mapping now; compaction triggers later.
	var cwConfigs contextwindow.ConfigSource
	if ldr != nil {
		cwConfigs = ldr
	}
	ctxWindows := contextwindow.NewResolver(registry, cwConfigs, mdClient)
	rt.SetContextWindows(ctxWindows)

	// Create the LocalService first (without graphExec — it needs svc as EventSink).
	svc := service.NewLocal(service.LocalConfig{
		AppConfig:        cfg,
		Store:            store,
		Runtime:          rt,
		MCPManager:       mcpManager,
		Provider:         client,
		Loader:           ldr,
		ConfigDir:        configDir,
		WorkspaceDir:     workspaceDir,
		OperatorModel:    cfg.Operator.Model,
		OperatorEndpoint: operatorEndpoint,
		StartTime:        time.Now(),
		Catalog:          catalog,
		Registry:         registry,
		PromptEngine:     promptEngine,
		DefaultProvider:  defaultProvider,
		DefaultModel:     defaultModel,
		GraphCatalog:     ldr,

		OperatorProviderID: cfg.Operator.Provider,
		ContextWindows:     ctxWindows,
	})
	defer svc.Shutdown()

	// Retry failed MCP servers in the background for the life of the service;
	// each recovery re-wires the runtime's MCP tool surface.
	if len(cfg.MCP.Servers) > 0 {
		mcpManager.StartReconnectLoop(svc.Ctx(), wireMCPTools)
	}

	// Wire the runtime's session-started callback to broadcast session events
	// through the service event stream. This is the only path by which worker
	// session activity reaches subscribers (TUI, SSE clients).
	rt.OnSessionStarted = svc.BroadcastSessionStarted

	// Create the graph executor for rhizome-based task execution.
	// Per-task tool executors are constructed inside ExecuteTask scoped to
	// each task's workspace directory (mirroring runtime.SpawnWorker) — the
	// MCP manager is long-lived and shared. The broker is shared with the
	// operator so ask_user from either path lands in the same TUI modal.
	execCfg := graphexec.ExecutorConfig{
		Registry:              registry,
		MCPManager:            mcpManager,
		PromptEngine:          promptEngine,
		Store:                 store,
		EventSink:             svc,
		Broker:                svc.Broker(),
		Graphs:                ldr,
		DefaultModel:          defaultModel,
		WorkerThinkingEnabled: cfg.WorkerThinkingEnabled,
		WorkerTemperature:     cfg.WorkerTemperature,
		ContextWindows:        ctxWindows,
		KBEnabled:             cfg.KB.Enabled,
	}
	// Enable node-granular checkpoint/resume when SQLite is the backend. Set
	// only when non-nil so the interface field stays a true nil (a typed-nil
	// *db.CheckpointStore would read as non-nil and break the executor's
	// checkpointing guard).
	if checkpoints != nil {
		execCfg.CheckpointStore = checkpoints
	}
	graphExec := graphexec.NewExecutor(execCfg)

	// Share the graph executor with the service so both the startup-time
	// operator (created just below) and any live-activated operator
	// (LocalService.startOperator, invoked when the user sets a provider
	// through the TUI) pick it up. Without this, live activation would
	// leave assignTask with no executor and tasks would error at dispatch.
	svc.SetGraphExecutor(graphExec)

	// Create and start the operator event loop.
	// Both a store and a provider are required — if the provider couldn't be
	// resolved (no providers configured, or operator.provider not found), the
	// operator is left nil and the TUI will show the disabled state.
	var op *operator.Operator
	if store != nil && client != nil {
		// activeTurn tracks the turn ID the operator is currently streaming
		// so timer-driven batch flushes stamp text with the right turn.
		// Turns are serial and OnTurnDone flushes both batchers before
		// clearing it, so a batch never straddles a turn boundary.
		var activeTurn atomic.Value
		activeTurn.Store("")
		textFlush := func(text string) {
			turnID, _ := activeTurn.Load().(string)
			svc.BroadcastOperatorText(turnID, text, "")
		}
		reasoningFlush := func(text string) {
			turnID, _ := activeTurn.Load().(string)
			svc.BroadcastOperatorText(turnID, "", text)
		}
		batcher := newTextBatcher(16*time.Millisecond, textFlush)
		reasoningBatcher := newTextBatcher(16*time.Millisecond, reasoningFlush)

		var opErr error
		op, opErr = operator.New(operator.Config{
			Runtime:                rt,
			Provider:               client,
			Model:                  cfg.Operator.Model,
			WorkDir:                workspaceDir,
			Store:                  store,
			SystemPrompt:           operatorPrompt,
			SessionFile:            filepath.Join(configDir, "sessions", "operator.json"),
			SystemEventBroadcaster: svc,
			GraphExecutor:          graphExec,
			GraphCatalog:           ldr,
			Broker:                 svc.Broker(),
			PromptEngine:           promptEngine,
			DefaultProvider:        defaultProvider,
			DefaultModel:           defaultModel,
			LifetimeCtx:            svc.Ctx(),
			ProviderID:             cfg.Operator.Provider,
			ContextWindows:         ctxWindows,
			CompactionThreshold:    config.ValidCompactionThreshold(cfg.OperatorCompactionThreshold, config.DefaultOperatorCompactionThreshold),
			OnText: func(turnID, text string) {
				activeTurn.Store(turnID)
				batcher.Add(text)
			},
			OnReasoning: func(turnID, text string) {
				activeTurn.Store(turnID)
				reasoningBatcher.Add(text)
			},
			OnEvent: func(event operator.Event) {
				svc.BroadcastOperatorEvent(event)
			},
			OnTurnDone: func(turnID string, tokensIn, tokensOut, reasoningTokens, contextTokens int) {
				reasoningBatcher.Flush()
				batcher.Flush()
				activeTurn.Store("")
				svc.BroadcastOperatorDone(turnID, cfg.Operator.Model, tokensIn, tokensOut, reasoningTokens, contextTokens)
			},
			// Without this, an operator started at boot (the case now that a
			// provider/model ship as config defaults) calls ask_user but the
			// prompt is never surfaced to the UI — it just hangs.
			OnPrompt:   svc.BroadcastOperatorPrompt,
			OnResolve:  svc.ResolveBlocker,
			OnToolCall: svc.BroadcastOperatorToolCall,
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
			// Loader.Load just reloaded the prompt engine from disk —
			// recompute engine-derived state that depends on config or other
			// definitions: the toolchain catalog global and the synthetic
			// granularity instructions (their source files may have changed).
			// cfg is the live config — UpdateSettings mutates it in place, so
			// runtime granularity changes are honored here.
			promptEngine.SetGlobal("available.toolchains", strings.Join(promptEngine.Toolchains(), ", "))
			if err := prompt.ApplyGranularity(promptEngine, "coarse", config.ValidGranularity("coarse", cfg.CoarseGranularity)); err != nil {
				slog.Warn("failed to reapply coarse_granularity after reload", "error", err)
			}
			if err := prompt.ApplyGranularity(promptEngine, "fine", config.ValidGranularity("fine", cfg.FineGranularity)); err != nil {
				slog.Warn("failed to reapply fine_granularity after reload", "error", err)
			}
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
// in the provider registry. Safe to call multiple times (hot-reload). Every
// provider is wrapped with a per-provider Scheduler so in-flight chat calls
// against the same backend are bounded — capacity comes from pc.Concurrency
// (defaulting to 1, which is safe for a local LLM).
// providerFingerprints tracks the last-registered config per provider key so
// hot reloads only swap schedulers whose config actually changed. Replacing
// an in-use scheduler transiently doubles the provider's effective
// concurrency cap (in-flight calls still hold the old scheduler's slots
// while the new one hands out a fresh set) — a real problem for
// resource-constrained local endpoints.
var (
	providerFingerprintsMu sync.Mutex
	providerFingerprints   = map[string]string{}
)

func registerProviders(registry *provider.Registry, ldr *loader.Loader) {
	if ldr == nil {
		return
	}
	providerFingerprintsMu.Lock()
	defer providerFingerprintsMu.Unlock()
	for _, pc := range ldr.Providers() {
		// Expand ${ENV_VAR} references in API key and endpoint.
		pc.APIKey = os.Expand(pc.APIKey, os.Getenv)
		pc.Endpoint = os.Expand(pc.Endpoint, os.Getenv)

		fp := fmt.Sprintf("%+v", pc)
		if providerFingerprints[pc.Key()] == fp {
			continue // unchanged — keep the existing scheduler and its held slots
		}

		p, err := provider.NewFromConfig(pc)
		if err != nil {
			slog.Warn("failed to create provider", "id", pc.ID, "name", pc.Name, "error", err)
			continue
		}
		scheduler := provider.NewScheduler(p, pc.Concurrency)
		slog.Info("registered provider", "id", pc.ID, "name", pc.Name,
			"concurrency", scheduler.Capacity())
		registry.Register(pc.Key(), scheduler)
		providerFingerprints[pc.Key()] = fp
	}
}
