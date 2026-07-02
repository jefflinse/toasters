package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/prompt"
)

// CatalogSource provides access to the models.dev catalog data.
// Implemented by modelsdev.Client.
type CatalogSource interface {
	ProvidersSorted(ctx context.Context) ([]CatalogSourceProvider, error)
}

// CatalogSourceProvider is the provider shape expected from the catalog source.
type CatalogSourceProvider struct {
	ID   string
	Name string
	API  string
	Doc  string
	Env  []string

	Models []CatalogSourceModel
}

// CatalogSourceModel is the model shape expected from the catalog source.
type CatalogSourceModel struct {
	ID               string
	Name             string
	Family           string
	ToolCall         bool
	Reasoning        bool
	StructuredOutput bool
	OpenWeights      bool
	ContextLimit     int
	OutputLimit      int
	InputCost        float64
	OutputCost       float64
}

// Health returns the current health status of the service.
func (s *LocalService) Health(_ context.Context) (HealthStatus, error) {
	return HealthStatus{
		Status:  "ok",
		Version: "0.1.0",
		Uptime:  time.Since(s.cfg.StartTime),
	}, nil
}

// ListModels returns all models available from the configured LLM provider.
func (s *LocalService) ListModels(ctx context.Context) ([]ModelInfo, error) {
	prov := s.currentProvider()
	if prov == nil {
		return nil, Unavailablef("LLM provider not configured")
	}
	provModels, err := prov.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	if s.cfg.ContextWindows != nil {
		providerID, _, _ := s.operatorInfo()
		if providerID != "" {
			s.cfg.ContextWindows.ObserveModels(providerID, provModels)
		}
	}
	models := make([]ModelInfo, 0, len(provModels))
	for _, m := range provModels {
		models = append(models, providerModelInfoToService(m))
	}
	return models, nil
}

// ListMCPServers returns the connection status for all configured MCP servers.
func (s *LocalService) ListMCPServers(_ context.Context) ([]MCPServerStatus, error) {
	if s.cfg.MCPManager == nil {
		return nil, nil
	}
	statuses := s.cfg.MCPManager.Servers()
	result := make([]MCPServerStatus, 0, len(statuses))
	for _, ss := range statuses {
		result = append(result, mcpServerStatusToService(ss))
	}
	return result, nil
}

// ConfigDir returns the configuration directory path. This is a local-only
// method, not part of the Service interface and not exposed over HTTP.
func (s *LocalService) ConfigDir() string {
	return s.cfg.ConfigDir
}

// GetProgressState returns the current full progress state snapshot. This is
// the on-demand hydration path (client connect/reconnect), so it gets a more
// generous budget than the 500ms poll and returns whatever it assembled even
// if the budget expired — for an explicit request, a partial snapshot beats
// an empty panel.
func (s *LocalService) GetProgressState(_ context.Context) (ProgressState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, _ := s.buildProgressState(ctx)
	return state, nil
}

// maxLogResponseBytes caps how much of the log file GetLogs returns. The log
// contains internal errors, filesystem paths, and tool output, so only a
// bounded tail goes over the API.
const maxLogResponseBytes = 256 * 1024

// GetLogs returns the tail of the application log file, capped at
// maxLogResponseBytes.
func (s *LocalService) GetLogs(_ context.Context) (string, error) {
	logPath := filepath.Join(s.cfg.ConfigDir, "toasters.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading log file: %w", err)
	}
	if len(data) > maxLogResponseBytes {
		data = data[len(data)-maxLogResponseBytes:]
		// Drop the (likely partial) first line of the truncated tail.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return string(data), nil
}

// ListCatalogProviders returns the full provider/model catalog from models.dev.
func (s *LocalService) ListCatalogProviders(ctx context.Context) ([]CatalogProvider, error) {
	if s.cfg.Catalog == nil {
		return nil, nil
	}
	provs, err := s.cfg.Catalog.ProvidersSorted(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing catalog providers: %w", err)
	}
	result := make([]CatalogProvider, 0, len(provs))
	for _, p := range provs {
		cp := CatalogProvider{
			ID:   p.ID,
			Name: p.Name,
			API:  p.API,
			Doc:  p.Doc,
			Env:  p.Env,
		}
		for _, m := range p.Models {
			cp.Models = append(cp.Models, CatalogModel{
				ID:               m.ID,
				Name:             m.Name,
				Family:           m.Family,
				ToolCall:         m.ToolCall,
				Reasoning:        m.Reasoning,
				StructuredOutput: m.StructuredOutput,
				OpenWeights:      m.OpenWeights,
				ContextLimit:     m.ContextLimit,
				OutputLimit:      m.OutputLimit,
				InputCost:        m.InputCost,
				OutputCost:       m.OutputCost,
			})
		}
		result = append(result, cp)
	}
	return result, nil
}

// AddProvider appends a new provider to config.yaml.
func (s *LocalService) AddProvider(_ context.Context, req AddProviderRequest) error {
	if req.ID == "" {
		return fmt.Errorf("provider ID is required")
	}
	if req.Name == "" {
		return fmt.Errorf("provider name is required")
	}
	if req.Type == "" {
		return fmt.Errorf("provider type is required")
	}
	switch req.Type {
	case "openai", "local", "anthropic":
	default:
		return fmt.Errorf("invalid provider type %q (must be openai, local, or anthropic)", req.Type)
	}

	if err := config.ValidateProviderID(req.ID); err != nil {
		return Invalidf("%s", err)
	}
	return config.AddProvider(s.cfg.ConfigDir, config.ProviderEntry{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
}

// UpdateProvider overwrites an existing provider YAML file.
func (s *LocalService) UpdateProvider(_ context.Context, req AddProviderRequest) error {
	if req.ID == "" {
		return fmt.Errorf("provider ID is required")
	}
	if err := config.ValidateProviderID(req.ID); err != nil {
		return Invalidf("%s", err)
	}
	return config.UpdateProvider(s.cfg.ConfigDir, config.ProviderEntry{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
}

// ListConfiguredProviderIDs returns the IDs of locally configured providers.
func (s *LocalService) ListConfiguredProviderIDs(_ context.Context) ([]string, error) {
	if s.cfg.Loader == nil {
		return nil, nil
	}
	provs := s.cfg.Loader.Providers()
	ids := make([]string, 0, len(provs))
	for _, p := range provs {
		ids = append(ids, p.Key())
	}
	return ids, nil
}

// SetOperatorProvider updates the operator provider ID in config.yaml and
// starts the operator live if a provider with that ID is in the registry.
func (s *LocalService) SetOperatorProvider(_ context.Context, providerID string, model string) error {
	if err := config.SetOperatorProvider(s.cfg.ConfigDir, providerID, model); err != nil {
		return err
	}

	// Update default provider/model so workers spawned after this change
	// inherit the operator's provider.
	s.opMu.Lock()
	s.defaultProvider = providerID
	s.defaultModel = model
	s.opMu.Unlock()

	// Attempt live activation.
	if s.cfg.Registry == nil {
		return nil
	}
	p, ok := s.cfg.Registry.Get(providerID)
	if !ok {
		slog.Warn("operator provider saved but not in registry yet; restart to activate", "provider", providerID)
		return nil
	}

	return s.startOperator(p, providerID, model)
}

// ListProviderModels returns models from a specific configured provider.
func (s *LocalService) ListProviderModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	if s.cfg.Registry == nil {
		return nil, fmt.Errorf("no provider registry")
	}
	p, ok := s.cfg.Registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerID)
	}
	provModels, err := p.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models for %q: %w", providerID, err)
	}
	if s.cfg.ContextWindows != nil {
		s.cfg.ContextWindows.ObserveModels(providerID, provModels)
	}
	models := make([]ModelInfo, 0, len(provModels))
	for _, m := range provModels {
		models = append(models, providerModelInfoToService(m))
	}
	return models, nil
}

// GetSettings returns the current user-editable runtime settings. Values are
// sourced from the in-memory config; if no config is wired (tests), sensible
// defaults are returned.
func (s *LocalService) GetSettings(_ context.Context) (Settings, error) {
	if s.cfg.AppConfig == nil {
		return Settings{
			CoarseGranularity:          config.ValidGranularity("coarse", ""),
			FineGranularity:            config.ValidGranularity("fine", ""),
			WorkerThinkingEnabled:      false,
			WorkerTemperature:          0.1,
			ShowJobsPanelByDefault:     false,
			ShowOperatorPanelByDefault: true,
			FleetRowDensity:            config.ValidFleetDensity(""),
			SidebarSide:                config.ValidSidebarSide(""),
		}, nil
	}
	return Settings{
		CoarseGranularity:          config.ValidGranularity("coarse", s.cfg.AppConfig.CoarseGranularity),
		FineGranularity:            config.ValidGranularity("fine", s.cfg.AppConfig.FineGranularity),
		WorkerThinkingEnabled:      s.cfg.AppConfig.WorkerThinkingEnabled,
		WorkerTemperature:          s.cfg.AppConfig.WorkerTemperature,
		ShowJobsPanelByDefault:     s.cfg.AppConfig.ShowJobsPanelByDefault,
		ShowOperatorPanelByDefault: s.cfg.AppConfig.ShowOperatorPanelByDefault,
		FleetRowDensity:            config.ValidFleetDensity(s.cfg.AppConfig.FleetRowDensity),
		SidebarSide:                config.ValidSidebarSide(s.cfg.AppConfig.SidebarSide),
	}, nil
}

// UpdateSettings validates, persists, and applies the given settings.
// Persistence writes to config.yaml in place; applying refreshes the prompt
// engine so new worker runs pick up the change immediately. Every granularity
// lever is validated before any write, so a bad value on one field leaves
// the rest untouched.
func (s *LocalService) UpdateSettings(_ context.Context, next Settings) error {
	if s.cfg.AppConfig == nil {
		return fmt.Errorf("settings unavailable: no app config loaded")
	}
	type lever struct {
		kind     string // "coarse" or "fine"
		yamlKey  string
		incoming string
		set      func(string)
	}
	levers := []lever{
		{
			kind:     "coarse",
			yamlKey:  "coarse_granularity",
			incoming: next.CoarseGranularity,
			set:      func(v string) { s.cfg.AppConfig.CoarseGranularity = v },
		},
		{
			kind:     "fine",
			yamlKey:  "fine_granularity",
			incoming: next.FineGranularity,
			set:      func(v string) { s.cfg.AppConfig.FineGranularity = v },
		},
	}

	// Validate EVERY field first, write second — so an invalid value on any
	// field (not just the granularity levers) leaves config.yaml untouched
	// rather than half-updated.
	for _, l := range levers {
		if normalized := config.ValidGranularity(l.kind, l.incoming); normalized != l.incoming {
			return Invalidf("invalid %s %q", l.yamlKey, l.incoming)
		}
	}
	if next.WorkerTemperature < 0 || next.WorkerTemperature > 2 {
		return Invalidf("invalid worker_temperature %v (must be in [0, 2])", next.WorkerTemperature)
	}

	for _, l := range levers {
		if err := config.SetTopLevelScalar(s.cfg.ConfigDir, l.yamlKey, l.incoming); err != nil {
			return fmt.Errorf("persisting %s: %w", l.yamlKey, err)
		}
		l.set(l.incoming)
		if s.cfg.PromptEngine != nil {
			if err := prompt.ApplyGranularity(s.cfg.PromptEngine, l.kind, l.incoming); err != nil {
				slog.Warn("failed to refresh granularity instruction", "kind", l.kind, "error", err)
			}
		}
	}

	// Worker defaults: persist as their native YAML types so viper
	// round-trips them as bool/float on next load. (Validated above.)
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "worker_thinking_enabled", next.WorkerThinkingEnabled); err != nil {
		return fmt.Errorf("persisting worker_thinking_enabled: %w", err)
	}
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "worker_temperature", next.WorkerTemperature); err != nil {
		return fmt.Errorf("persisting worker_temperature: %w", err)
	}
	s.cfg.AppConfig.WorkerThinkingEnabled = next.WorkerThinkingEnabled
	s.cfg.AppConfig.WorkerTemperature = next.WorkerTemperature
	if applier, ok := s.currentGraphExecutor().(workerDefaultsApplier); ok {
		applier.SetWorkerDefaults(next.WorkerThinkingEnabled, next.WorkerTemperature)
	}

	// Panel visibility defaults: pure UI prefs, no live engine to refresh.
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "show_jobs_panel_by_default", next.ShowJobsPanelByDefault); err != nil {
		return fmt.Errorf("persisting show_jobs_panel_by_default: %w", err)
	}
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "show_operator_panel_by_default", next.ShowOperatorPanelByDefault); err != nil {
		return fmt.Errorf("persisting show_operator_panel_by_default: %w", err)
	}
	s.cfg.AppConfig.ShowJobsPanelByDefault = next.ShowJobsPanelByDefault
	s.cfg.AppConfig.ShowOperatorPanelByDefault = next.ShowOperatorPanelByDefault

	density := config.ValidFleetDensity(next.FleetRowDensity)
	if err := config.SetTopLevelScalar(s.cfg.ConfigDir, "fleet_row_density", density); err != nil {
		return fmt.Errorf("persisting fleet_row_density: %w", err)
	}
	s.cfg.AppConfig.FleetRowDensity = density

	side := config.ValidSidebarSide(next.SidebarSide)
	if err := config.SetTopLevelScalar(s.cfg.ConfigDir, "sidebar_side", side); err != nil {
		return fmt.Errorf("persisting sidebar_side: %w", err)
	}
	s.cfg.AppConfig.SidebarSide = side

	return nil
}
