package cmd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/bootstrap"
	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
	"github.com/jefflinse/toasters/internal/service"
	"github.com/jefflinse/toasters/internal/tui"
)

var rootCmd = &cobra.Command{
	Use:   "toasters",
	Short: "A TUI orchestrator for agentic coding work",
	RunE:  runTUI,
}

func init() {
	rootCmd.Flags().String("operator-endpoint", "", "LM Studio endpoint URL (overrides config)")
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

	teamsDir := cfg.Operator.TeamsDir

	// Open SQLite database for persistence (graceful degradation if it fails).
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
		composer = compose.New(store, cfg.Agents.DefaultProvider, cfg.Agents.DefaultModel)
	}

	// Create provider registry and register configured providers.
	registry := provider.NewRegistry()
	for _, pc := range cfg.Providers {
		p, provErr := provider.NewFromConfig(pc)
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

	var client provider.Provider
	switch cfg.Operator.Provider {
	case "anthropic":
		client = provider.NewAnthropic("anthropic", "", provider.WithAnthropicModel(cfg.Operator.Model))
	default:
		client = provider.NewOpenAI("operator", cfg.Operator.Endpoint, "", cfg.Operator.Model)
	}

	// p holds the Bubble Tea program pointer; set before operator starts so
	// callbacks can safely call p.Load() and find a non-nil program.
	var p atomic.Pointer[tea.Program]

	// notifySessionStarted wires a runtime session into the TUI event loop.
	// It is used for all sessions — both coordinator sessions (spawned by assign_team)
	// and child sessions (spawned by spawn_agent) — via rt.OnSessionStarted.
	notifySessionStarted := func(sess *runtime.Session) {
		snap := sess.Snapshot()
		if prog := p.Load(); prog != nil {
			prog.Send(tui.RuntimeSessionStartedMsg{
				SessionID:      snap.ID,
				AgentName:      snap.AgentID,
				TeamName:       snap.TeamName,
				Task:           sess.Task(),
				JobID:          snap.JobID,
				TaskID:         snap.TaskID,
				SystemPrompt:   sess.SystemPrompt(),
				InitialMessage: sess.InitialMessage(),
			})
		}

		// Forward session events in a goroutine.
		go func() {
			events := sess.Subscribe()
			for ev := range events {
				if prog := p.Load(); prog != nil {
					var msg tui.RuntimeSessionEventMsg
					switch ev.Type {
					case runtime.SessionEventText:
						msg = tui.RuntimeSessionEventMsg{
							SessionID: snap.ID,
							EventType: "text",
							Text:      ev.Text,
						}
					case runtime.SessionEventToolCall:
						msg = tui.RuntimeSessionEventMsg{
							SessionID: snap.ID,
							EventType: "tool_call",
							ToolName:  ev.ToolCall.Name,
							ToolInput: string(ev.ToolCall.Arguments),
						}
					case runtime.SessionEventToolResult:
						msg = tui.RuntimeSessionEventMsg{
							SessionID:  snap.ID,
							EventType:  "tool_result",
							ToolOutput: ev.ToolResult.Result,
							IsError:    ev.ToolResult.Error != "",
						}
					default:
						continue
					}
					prog.Send(msg)
				}
			}
			// Session done — send completion message.
			finalSnap := sess.Snapshot()
			if prog := p.Load(); prog != nil {
				prog.Send(tui.RuntimeSessionDoneMsg{
					SessionID: finalSnap.ID,
					AgentName: finalSnap.AgentID,
					JobID:     finalSnap.JobID,
					TaskID:    finalSnap.TaskID,
					FinalText: sess.FinalText(),
					Status:    finalSnap.Status,
				})
			}
		}()
	}

	// Wire the callback on the runtime so all sessions (coordinator + children)
	// are forwarded to the TUI through a single path.
	rt.OnSessionStarted = notifySessionStarted

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

	// Create the LocalService — the single service boundary between TUI and engine.
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
				// TODO(Phase 2): Token counts are hardcoded to 0 because
				// operator.Config.OnTurnDone does not receive token usage
				// from the LLM response. The operator needs to be updated
				// to pass actual tokensIn/tokensOut/reasoningTokens here.
				// Until then, sidebar stats (prompt ctx, tokens out,
				// reasoning, speed) will not update from real data.
				svc.BroadcastOperatorDone(cfg.Operator.Model, 0, 0, 0)
			},
		})
		if opErr != nil {
			slog.Warn("failed to create operator", "error", opErr)
		}
	}

	// Wire the operator into the service after creation. The operator's
	// callbacks already close over svc, so we just need to inject the
	// operator reference into the service to complete the circular wiring.
	if op != nil {
		svc.SetOperator(op)
	}

	m := tui.NewModel(tui.ModelConfig{
		Service:   svc,
		ConfigDir: configDir,
	})

	prog := tea.NewProgram(&m)
	// Store prog BEFORE starting the operator so that notifySessionStarted and
	// all operator callbacks can safely call p.Load() and find a non-nil program.
	p.Store(prog)

	if op != nil {
		opCtx, opCancel := context.WithCancel(context.Background())
		defer opCancel()
		op.Start(opCtx)
	}

	// Start the service event consumer — translates service events to TUI messages.
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	go tui.ConsumeServiceEvents(consumerCtx, svc, &p)

	// Generate team awareness and send the operator greeting in the background.
	go func() {
		ctx := context.Background()

		// Fetch initial teams from the service for awareness generation.
		initialTeams, _ := svc.Definitions().ListTeams(ctx)
		awareness := generateTeamAwareness(ctx, client, initialTeams, configDir)

		if op != nil {
			if prog := p.Load(); prog != nil {
				prog.Send(tui.AppReadyMsg{Awareness: awareness, Greeting: ""})
			}
			// Send the greeting through the operator.
			if err := op.Send(ctx, operator.Event{
				Type: operator.EventUserMessage,
				Payload: operator.UserMessagePayload{
					Text: "Greet the user briefly. One or two sentences max. Be direct and ready to work.",
				},
			}); err != nil {
				slog.Warn("failed to send greeting through operator", "error", err)
			}
		} else {
			if prog := p.Load(); prog != nil {
				prog.Send(tui.AppReadyMsg{
					Awareness: awareness,
					Greeting:  "⚠ Database unavailable — operator is offline. Check ~/.config/toasters/toasters.log for details.",
				})
			}
		}
	}()

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	// Watch for definition file changes and reload.
	if ldr != nil {
		defWatcher, defWatchErr := loader.NewWatcher(ldr, func() {
			svc.BroadcastDefinitionsReloaded()
			// Rebuild team views and send TeamsReloadedMsg.
			ctx := context.Background()
			newTeams, _ := svc.Definitions().ListTeams(ctx)
			newAwareness := generateTeamAwareness(ctx, client, newTeams, configDir)
			if prog := p.Load(); prog != nil {
				prog.Send(tui.TeamsReloadedMsg{Teams: newTeams, Awareness: newAwareness})
			}
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
	p.Store(nil) // Prevent post-shutdown sends
	return err
}
