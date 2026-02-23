package cmd

import (
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

	// Discover agents from ~/.opencode/agents.
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agentsDir := filepath.Join(home, ".opencode", "agents")
	discovered, err := agents.Discover(agentsDir)
	if err != nil {
		// Non-fatal: log a warning and proceed with no agents.
		log.Printf("warning: failed to discover agents in %s: %v", agentsDir, err)
		discovered = []agents.Agent{}
	}
	registry := agents.BuildRegistry(discovered, cfg.Operator.CoordinatorAgent)

	// Build a flat map of all agents (coordinator + workers) for the gateway.
	agentMap := make(map[string]agents.Agent, len(registry.Workers)+1)
	for _, a := range registry.Workers {
		agentMap[a.Name] = a
	}
	if registry.Coordinator != nil {
		agentMap[registry.Coordinator.Name] = *registry.Coordinator
	}

	// Create the gateway with a no-op notify for now.
	// The TUI will replace this with a real notify after the program starts.
	gw := gateway.New(cfg.Claude, repoRoot, func() {})
	gw.SetAgentRegistry(agentMap)
	llm.SetGateway(gw)
	llm.SetAvailableTools(llm.BuildTools(registry.Workers))

	client := llm.NewClient(cfg.Operator.Endpoint, cfg.Operator.Model)
	m := tui.NewModel(client, cfg.Claude, configDir, gw, repoRoot, registry)

	_, err = tea.NewProgram(m).Run()
	return err
}
