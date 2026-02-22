package cmd

import (
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/internal/config"
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

	client := llm.NewClient(cfg.Operator.Endpoint, cfg.Operator.Model)
	m := tui.NewModel(client, cfg.Claude)

	_, err = tea.NewProgram(m).Run()
	return err
}
