package cmd

import (
	"context"
	"log"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/llm"
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
	gw := gateway.New(cfg.Claude, repoRoot, func() {})
	llm.SetGateway(gw)
	llm.SetTeams(teams)

	client := llm.NewClient(cfg.Operator.Endpoint, cfg.Operator.Model)
	if cfg.Operator.LogRequests {
		client.SetRequestLogging(true, filepath.Join(configDir, "requests.log"))
	}

	awareness := generateTeamAwareness(context.Background(), client, teams, configDir)

	m := tui.NewModel(client, cfg.Claude, configDir, gw, repoRoot, teamsDir, teams, awareness)

	p := tea.NewProgram(&m)

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
			llm.SetTeams(allTeams)
			newAwareness := generateTeamAwareness(context.Background(), client, allTeams, configDir)
			p.Send(tui.TeamsReloadedMsg{Teams: allTeams, Awareness: newAwareness})
		})
		if err != nil && watchCtx.Err() == nil {
			log.Printf("teams watcher error: %v", err)
		}
	}()

	_, err = p.Run()
	return err
}
