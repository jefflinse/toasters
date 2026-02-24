package cmd

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/anthropic"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
	llmclient "github.com/jefflinse/toasters/internal/llm/client"
	llmtools "github.com/jefflinse/toasters/internal/llm/tools"
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

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	configDir, err := config.Dir()
	if err != nil {
		return err
	}

	// Redirect the default logger to a file so log.Printf calls don't
	// corrupt the TUI's alt-screen. Logs go to ~/.config/toasters/toasters.log.
	if err := os.MkdirAll(configDir, 0755); err == nil {
		logPath := filepath.Join(configDir, "toasters.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			log.SetOutput(f)
			defer func() { _ = f.Close() }()
		} else {
			// Can't open log file — discard logs rather than corrupt the TUI.
			log.SetOutput(io.Discard)
		}
	} else {
		log.SetOutput(io.Discard)
	}

	workspaceDir, err := config.WorkspaceDir(cfg)
	if err != nil {
		return err
	}

	// repoRoot is the directory containing the agents/ folder.
	// For now, use the current working directory.
	repoRoot, err := os.Getwd()
	if err != nil {
		return err
	}

	// Discover teams from the configured teams directory.
	teamsDir := cfg.Operator.TeamsDir
	teams, err := agents.DiscoverTeams(teamsDir)
	if err != nil {
		// Non-fatal: log a warning and proceed with no teams.
		log.Printf("warning: failed to discover teams in %s: %v", teamsDir, err)
		teams = []agents.Team{}
	}

	// Also include auto-detected teams (e.g. ~/.opencode/agents, ~/.claude/agents).
	autoTeams := agents.AutoDetectTeams()
	teams = append(teams, autoTeams...)

	// Create the gateway with a no-op notify for now.
	// The TUI will replace this with a real notify after the program starts.
	gw := gateway.New(cfg.Claude, workspaceDir, func() {})
	toolExec := llmtools.NewToolExecutor(gw, teams, workspaceDir)

	var client llm.Provider
	switch cfg.Operator.Provider {
	case "anthropic":
		client = anthropic.NewClient(cfg.Operator.Model)
	default:
		client = llmclient.NewClient(cfg.Operator.Endpoint, cfg.Operator.Model)
	}

	m := tui.NewModel(client, cfg.Claude, workspaceDir, gw, repoRoot, teamsDir, teams, "", toolExec)

	p := tea.NewProgram(&m)

	gw.SetSend(func(msg gateway.SlotTimeoutMsg) {
		p.Send(msg)
	})

	// Generate team awareness and pre-fetch the operator greeting in the background
	// so the TUI appears immediately. Always send AppReadyMsg even on error.
	go func() {
		ctx := context.Background()
		awareness := generateTeamAwareness(ctx, client, teams, configDir)

		// Pre-fetch greeting so it renders instantly when the loading screen clears.
		systemPrompt := agents.BuildOperatorPrompt(teams, awareness)
		greeting, err := client.ChatCompletion(ctx, []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: "Greet the user briefly. One or two sentences max. Be direct and ready to work."},
		})
		if err != nil {
			log.Printf("failed to pre-fetch greeting: %v", err)
		}

		p.Send(tui.AppReadyMsg{Awareness: awareness, Greeting: greeting})
	}()

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go func() {
		err := agents.Watch(watchCtx, teamsDir, func() {
			newTeams, err := agents.DiscoverTeams(teamsDir)
			if err != nil {
				log.Printf("teams: reload error: %v", err)
				return
			}
			autoTeams := agents.AutoDetectTeams()
			allTeams := append(newTeams, autoTeams...)
			toolExec.Teams = allTeams
			newAwareness := generateTeamAwareness(context.Background(), client, allTeams, configDir)
			p.Send(tui.TeamsReloadedMsg{Teams: allTeams, Awareness: newAwareness})
		})
		if err != nil && watchCtx.Err() == nil {
			log.Printf("teams watcher error: %v", err)
		}
	}()

	go func() {
		jobsDir := job.JobsDir(workspaceDir)
		err := agents.WatchRecursive(watchCtx, jobsDir, func() {
			jobs, err := job.List(workspaceDir)
			if err != nil {
				log.Printf("jobs: reload error: %v", err)
				return
			}
			p.Send(tui.JobsReloadedMsg{Jobs: jobs})
		})
		if err != nil {
			log.Printf("jobs watcher error: %v", err)
		}
	}()

	_, err = p.Run()
	return err
}
