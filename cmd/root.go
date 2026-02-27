package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/bootstrap"
	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/gateway"
	llmtools "github.com/jefflinse/toasters/internal/llm/tools"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
	"github.com/jefflinse/toasters/internal/tui"
)

var rootCmd = &cobra.Command{
	Use:   "toasters",
	Short: "A TUI orchestrator for agentic coding work",
	RunE:  runTUI,
}

func init() {
	rootCmd.Flags().String("operator-endpoint", "", "LM Studio endpoint URL (overrides config)")
	rootCmd.Flags().String("claude-path", "", "Path to the claude binary (overrides config)")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func runTUI(cmd *cobra.Command, _ []string) error {
	config.BindFlags(cmd)

	configDir, err := config.Dir()
	if err != nil {
		return err
	}

	// Bootstrap runs before config.Load() so that the default config.yaml is
	// written to disk before Viper reads it. On first run this ensures the
	// operator and provider settings from the embedded default are picked up
	// rather than Viper's built-in fallback defaults.
	if err := bootstrap.Run(configDir, defaults.SystemFiles, defaults.DefaultConfig); err != nil {
		// Non-fatal — log to stderr since the slog handler isn't set up yet.
		slog.Warn("bootstrap failed", "error", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Redirect slog output to a file so structured log messages don't
	// corrupt the TUI's alt-screen. Logs go to ~/.config/toasters/toasters.log.
	if err := os.MkdirAll(configDir, 0755); err == nil {
		logPath := filepath.Join(configDir, "toasters.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
			defer func() { _ = f.Close() }()
		} else {
			// Can't open log file — discard logs rather than corrupt the TUI.
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	workspaceDir, err := config.WorkspaceDir(cfg)
	if err != nil {
		return err
	}

	// Discover teams from the configured teams directory.
	teamsDir := cfg.Operator.TeamsDir
	teams, err := agents.DiscoverTeams(teamsDir)
	if err != nil {
		// Non-fatal: log a warning and proceed with no teams.
		slog.Warn("failed to discover teams", "path", teamsDir, "error", err)
		teams = []agents.Team{}
	}

	// Also include auto-detected teams (e.g. ~/.opencode/agents, ~/.claude/agents).
	autoTeams := agents.AutoDetectTeams()
	teams = append(teams, autoTeams...)

	// Open SQLite database for persistence (graceful degradation if it fails).
	var store db.Store
	dbPath, err := config.DatabasePath(cfg)
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
		composer = compose.New(store, cfg.Agents.DefaultProvider, cfg.Agents.DefaultModel)
	}

	// Create provider registry and register configured providers.
	registry := provider.NewRegistry()
	for _, pc := range cfg.Providers {
		provCfg := provider.ProviderConfig{
			Name:     pc.Name,
			Type:     pc.Type,
			Endpoint: pc.Endpoint,
			APIKey:   pc.APIKey,
			Model:    pc.Model,
		}
		p, provErr := provider.NewFromConfig(provCfg)
		if provErr != nil {
			slog.Warn("failed to create provider", "provider", pc.Name, "error", provErr)
			continue
		}
		registry.Register(pc.Name, p)
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

	// Create the gateway with a no-op notify for now.
	// The TUI will replace this with a real notify after the program starts.
	gw := gateway.New(cfg.Claude, workspaceDir, func() {})
	if dbPath != "" {
		gw.SetDBPath(dbPath)
	}
	toolExec := llmtools.NewToolExecutor(gw, teams, workspaceDir, store, rt)
	toolExec.DefaultProvider = cfg.Agents.DefaultProvider
	toolExec.DefaultModel = cfg.Agents.DefaultModel

	// Wire MCP tools into operator tool set.
	toolExec.MCPManager = mcpManager
	toolExec.Tools = append(toolExec.Tools, mcp.ToProviderTools(mcpManager.Tools())...)

	var client provider.Provider
	switch cfg.Operator.Provider {
	case "anthropic":
		client = provider.NewAnthropic("anthropic", "", provider.WithAnthropicModel(cfg.Operator.Model))
	default:
		client = provider.NewOpenAI("operator", cfg.Operator.Endpoint, "", cfg.Operator.Model)
	}

	// Create and start the operator event loop.
	// The operator is created before the TUI program so we can wire callbacks.
	// Callbacks use p.Send() which is safe to call before p.Run() — messages
	// are buffered until the program starts.
	var op *operator.Operator
	var p atomic.Pointer[tea.Program]

	// notifySessionStarted wires a runtime session into the TUI event loop.
	// It is used for all sessions — both coordinator sessions (spawned by assign_team)
	// and child sessions (spawned by spawn_agent) — via rt.OnSessionStarted.
	// Defined before operator.Start() to avoid a data race on the callback.
	notifySessionStarted := func(sess *runtime.Session) {
		snap := sess.Snapshot()
		if prog := p.Load(); prog != nil {
			prog.Send(tui.RuntimeSessionStartedMsg{
				SessionID:      snap.ID,
				AgentName:      snap.AgentID,
				TeamName:       snap.TeamName,
				Task:           sess.Task(),
				JobID:          snap.JobID,
				SystemPrompt:   sess.SystemPrompt(),
				InitialMessage: sess.InitialMessage(),
			})
		}

		// Forward events in a goroutine.
		go func() {
			events := sess.Subscribe()
			for ev := range events {
				if prog := p.Load(); prog != nil {
					prog.Send(tui.RuntimeSessionEventMsg{Event: ev})
				}
			}
			// Session done — send completion message.
			finalSnap := sess.Snapshot()
			if prog := p.Load(); prog != nil {
				prog.Send(tui.RuntimeSessionDoneMsg{
					SessionID: finalSnap.ID,
					AgentName: finalSnap.AgentID,
					JobID:     finalSnap.JobID,
					FinalText: sess.FinalText(),
					Status:    finalSnap.Status,
				})
			}
		}()
	}

	// Wire the callback on the runtime so all sessions (coordinator + children)
	// are forwarded to the TUI through a single path.
	rt.OnSessionStarted = notifySessionStarted

	if store != nil {
		op = operator.New(operator.Config{
			Runtime:  rt,
			Provider: client,
			Model:    cfg.Operator.Model,
			WorkDir:  workspaceDir,
			Store:    store,
			Composer: composer,
			Spawner:  rt,
			OnText: func(text string) {
				if prog := p.Load(); prog != nil {
					prog.Send(tui.OperatorTextMsg{Text: text})
				}
			},
			OnEvent: func(event operator.Event) {
				if prog := p.Load(); prog != nil {
					prog.Send(tui.OperatorEventMsg{Event: event})
				}
			},
			OnTurnDone: func() {
				if prog := p.Load(); prog != nil {
					prog.Send(tui.OperatorDoneMsg{})
				}
			},
		})
	}

	m := tui.NewModel(tui.ModelConfig{
		Client:        client,
		ClaudeCfg:     cfg.Claude,
		WorkspaceDir:  workspaceDir,
		Gateway:       gw,
		TeamsDir:      teamsDir,
		Teams:         teams,
		Awareness:     "",
		ToolExec:      toolExec,
		Store:         store,
		Runtime:       rt,
		MCPManager:    mcpManager,
		Operator:      op,
		OperatorModel: cfg.Operator.Model,
	})

	prog := tea.NewProgram(&m)
	// Store prog BEFORE starting the operator so that notifySessionStarted and
	// all operator callbacks can safely call p.Load() and find a non-nil program.
	// Bubble Tea buffers Send() calls made before Run() starts, so this is safe.
	p.Store(prog)

	if op != nil {
		opCtx, opCancel := context.WithCancel(context.Background())
		defer opCancel()
		op.Start(opCtx)
	}

	gw.SetSend(func(msg gateway.SlotTimeoutMsg) {
		prog.Send(msg)
	})

	// Generate team awareness and (optionally) pre-fetch the operator greeting in
	// the background so the TUI appears immediately. Always send AppReadyMsg even
	// on error.
	go func() {
		ctx := context.Background()
		awareness := generateTeamAwareness(ctx, client, teams, configDir)

		if op != nil {
			// When the operator is active, skip the pre-fetched greeting.
			// Instead, send a greeting request through the operator so it goes
			// through the operator's conversation history and streams naturally.
			prog.Send(tui.AppReadyMsg{Awareness: awareness, Greeting: ""})

			// Send the greeting through the operator after AppReadyMsg so the
			// TUI is ready to receive OperatorTextMsg / OperatorDoneMsg.
			if err := op.Send(ctx, operator.Event{
				Type: operator.EventUserMessage,
				Payload: operator.UserMessagePayload{
					Text: "Greet the user briefly. One or two sentences max. Be direct and ready to work.",
				},
			}); err != nil {
				slog.Warn("failed to send greeting through operator", "error", err)
			}
			return
		}

		// Legacy path: pre-fetch greeting via direct API call.
		systemPrompt := agents.BuildOperatorPrompt(teams, awareness)
		greeting, err := provider.ChatCompletion(ctx, client, []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: "Greet the user briefly. One or two sentences max. Be direct and ready to work."},
		})
		if err != nil {
			slog.Warn("failed to pre-fetch greeting", "error", err)
		}

		prog.Send(tui.AppReadyMsg{Awareness: awareness, Greeting: greeting})
	}()

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go func() {
		err := agents.Watch(watchCtx, teamsDir, func() {
			newTeams, err := agents.DiscoverTeams(teamsDir)
			if err != nil {
				slog.Error("teams reload failed", "error", err)
				return
			}
			autoTeams := agents.AutoDetectTeams()
			allTeams := append(newTeams, autoTeams...)
			toolExec.SetTeams(allTeams)
			newAwareness := generateTeamAwareness(context.Background(), client, allTeams, configDir)
			prog.Send(tui.TeamsReloadedMsg{Teams: allTeams, Awareness: newAwareness})
		})
		if err != nil && watchCtx.Err() == nil {
			slog.Error("teams watcher error", "error", err)
		}
	}()

	// Watch for definition file changes and reload.
	if ldr != nil {
		defWatcher, defWatchErr := loader.NewWatcher(ldr, func() {
			prog.Send(tui.DefinitionsReloadedMsg{})
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

	_, err = prog.Run()
	return err
}
