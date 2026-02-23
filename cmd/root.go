package cmd

import (
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

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

	// Create the gateway with a no-op notify for now.
	// The TUI will replace this with a real notify after the program starts.
	gw := gateway.New(cfg.Claude, repoRoot, func() {})
	llm.SetGateway(gw)

	client := llm.NewClient(cfg.Operator.Endpoint, cfg.Operator.Model)
	m := tui.NewModel(client, cfg.Claude, configDir, gw, repoRoot)

	_, err = tea.NewProgram(m).Run()
	return err
}
