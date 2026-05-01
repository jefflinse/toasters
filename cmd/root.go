package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/jefflinse/toasters/internal/auth"
	"github.com/jefflinse/toasters/internal/client"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/service"
	"github.com/jefflinse/toasters/internal/tui"
)

// defaultServerAddr is used when --server is not specified.
const defaultServerAddr = "localhost:8421"

var serverAddr string

var rootCmd = &cobra.Command{
	Use:   "toasters",
	Short: "A TUI orchestrator for agentic coding work",
	Long: `Toasters is a TUI client for an agentic orchestration server.

The server runs separately and the TUI connects to it over HTTP+SSE. Start a
server with "toasters serve", then run "toasters" to connect.`,
	RunE: runTUI,
}

func init() {
	rootCmd.Flags().StringVar(&serverAddr, "server", defaultServerAddr,
		"address of the toasters server (host:port)")
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

	// Redirect slog output to a file so structured log messages don't
	// corrupt the TUI's alt-screen. Use a CLIENT-only log file so we don't
	// race the server's logger on the same file. Truncate on startup so each
	// session has a clean log to inspect after a freeze.
	if err := os.MkdirAll(configDir, 0755); err == nil {
		logPath := filepath.Join(configDir, "client.log")
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer func() { _ = f.Close() }()
		} else {
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		}
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	// Reachability probe — give a friendly error before opening the alt-screen
	// rather than letting Bubble Tea start and then strand the user on a loading
	// screen with an SSE connection error.
	if err := probeServer(serverAddr); err != nil {
		return fmt.Errorf("cannot reach toasters server at %s: %w\n\nStart a server with: toasters serve", serverAddr, err)
	}

	token, _ := auth.LoadToken(configDir)
	rc, err := client.New("http://"+serverAddr, client.WithToken(token))
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	defer rc.Close()

	var svc service.Service = rc

	m := tui.NewModel(tui.ModelConfig{
		Service:      svc,
		OpenInEditor: openInEditor,
	})

	prog := tea.NewProgram(&m)
	var p atomic.Pointer[tea.Program]
	p.Store(prog)

	// Start the service event consumer — translates service events to TUI messages.
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	go tui.ConsumeServiceEvents(consumerCtx, svc, &p)

	// Hydrate teams + transition out of the loading screen as soon as the
	// connection is established. Run in a goroutine so the TUI can render
	// while the initial fetch happens.
	go sendInitialAppReady(svc, &p, serverAddr)

	_, err = prog.Run()
	p.Store(nil) // Prevent post-shutdown sends
	return err
}

// probeServer attempts a fast TCP dial to the server address to confirm it is
// reachable before launching the TUI. Returns nil on success.
func probeServer(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// sendInitialAppReady fetches initial state from the server and sends the
// AppReadyMsg that transitions the TUI out of the loading screen. Runs in
// its own goroutine.
func sendInitialAppReady(svc service.Service, p *atomic.Pointer[tea.Program], serverAddr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Pull operator status so the sidebar shows the canonical model name and
	// endpoint URL from the server config (rather than whatever ListModels
	// happens to return first).
	var modelName, endpoint string
	var operatorDisabled bool
	if status, err := svc.Operator().Status(ctx); err == nil {
		modelName = status.ModelName
		endpoint = status.Endpoint
		operatorDisabled = status.State == service.OperatorStateDisabled
	} else {
		slog.Warn("failed to fetch operator status during startup", "error", err)
	}

	// Pull persisted chat history so the conversation survives a server
	// restart. Best-effort — an empty list is fine.
	history, err := svc.Operator().History(ctx)
	if err != nil {
		slog.Warn("failed to fetch chat history during startup", "error", err)
	}

	// Wait briefly for SSE connection to stabilize so the consumer is wired
	// before any startup events arrive.
	time.Sleep(200 * time.Millisecond)

	if prog := p.Load(); prog != nil {
		greeting := "Connected to " + serverAddr
		if operatorDisabled {
			greeting = ""
		}
		prog.Send(tui.AppReadyMsg{
			Greeting:         greeting,
			ModelName:        modelName,
			Endpoint:         endpoint,
			History:          history,
			OperatorDisabled: operatorDisabled,
		})
	}
}

// openInEditor launches $EDITOR (or vi) for the given file path, suspending the TUI.
func openInEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return tui.EditorFinishedMsg{Err: err}
	})
}
