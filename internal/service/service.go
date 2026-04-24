package service

import "context"

// ---------------------------------------------------------------------------
// Top-level Service interface
// ---------------------------------------------------------------------------

// Service is the top-level interface for the Toasters orchestration engine.
// It is composed of domain-specific sub-interfaces, each covering a distinct
// area of functionality. The TUI holds a single Service value and calls
// sub-interface methods for all operations.
//
// Two implementations exist:
//   - LocalService: in-process, delegates to db.Store, operator.Operator,
//     runtime.Runtime, mcp.Manager, etc. Used in embedded mode.
//   - RemoteClient: over HTTP + SSE, used when connecting to a standalone
//     toasters server.
//
// The TUI must not import any internal package other than service. All
// business logic lives in the implementation, not the interface.
type Service interface {
	// Operator returns the sub-interface for sending messages and managing
	// the operator LLM conversation.
	Operator() OperatorService

	// Definitions returns the sub-interface for managing skills and workers
	// (CRUD, LLM-powered skill generation).
	Definitions() DefinitionService

	// Jobs returns the sub-interface for listing, inspecting, and cancelling jobs.
	Jobs() JobService

	// Sessions returns the sub-interface for listing and inspecting worker sessions.
	Sessions() SessionService

	// Events returns the sub-interface for subscribing to the unified event stream.
	Events() EventService

	// System returns the sub-interface for health checks, model listing, and
	// MCP server status.
	System() SystemService
}

// ---------------------------------------------------------------------------
// OperatorService
// ---------------------------------------------------------------------------

// OperatorService handles the operator LLM conversation and user interaction.
type OperatorService interface {
	// SendMessage sends a user message to the operator event loop and returns
	// a turnID that correlates subsequent operator.text and operator.done events
	// back to this message. The method returns immediately — the operator
	// processes the message asynchronously and pushes events via the event stream.
	//
	// The TUI should enter streaming state after calling SendMessage and exit
	// streaming state when it receives an operator.done event with the matching
	// turnID.
	SendMessage(ctx context.Context, message string) (turnID string, err error)

	// RespondToPrompt sends the user's answer to an active ask_user prompt.
	// requestID must match the RequestID from the OperatorPromptPayload that
	// triggered the prompt. The response is delivered directly to the operator
	// (not via the event channel) to avoid deadlocking the event loop.
	RespondToPrompt(ctx context.Context, requestID string, response string) error

	// Status returns the current state of the operator (idle, streaming,
	// processing) and the model it is using. Used by clients to rebuild state
	// on reconnect (B4 concern).
	Status(ctx context.Context) (OperatorStatus, error)

	// History returns the conversation history for the current session.
	// Used by remote clients to hydrate the chat view on reconnect.
	// Returns entries in chronological order (oldest first).
	// The history is bounded to the most recent maxHistoryEntries entries.
	History(ctx context.Context) ([]ChatEntry, error)
}

// ---------------------------------------------------------------------------
// DefinitionService
// ---------------------------------------------------------------------------

// DefinitionService manages skills and workers. It covers all CRUD operations
// and LLM-powered skill generation.
//
// Generation operations are async: they return an operationID immediately and
// push operation.completed or operation.failed events via the event stream
// when done. The TUI should show a "generating…" indicator and update its
// state when the event arrives.
type DefinitionService interface {
	// --- Skills ---

	// ListSkills returns all skills known to the service, ordered by source
	// (user first, then system) and then by name.
	ListSkills(ctx context.Context) ([]Skill, error)

	// GetSkill returns a single skill by ID. Returns an error wrapping
	// ErrNotFound if the skill does not exist.
	GetSkill(ctx context.Context, id string) (Skill, error)

	// CreateSkill creates a new skill with the given name. It writes a template
	// .md file to the user skills directory and triggers a definition reload.
	// Returns the created skill.
	CreateSkill(ctx context.Context, name string) (Skill, error)

	// DeleteSkill deletes the skill with the given ID by removing its source
	// file. System skills (Source == "system") cannot be deleted and return an
	// error. Triggers a definition reload.
	DeleteSkill(ctx context.Context, id string) error

	// GenerateSkill asks the LLM to generate a skill definition for the given
	// prompt. Returns an operationID immediately. When the generation completes,
	// an operation.completed event is pushed with Kind == "generate_skill" and
	// the generated content in Result.Content. The implementation writes the
	// file and triggers a reload before pushing the event.
	//
	// On failure, an operation.failed event is pushed with the error message.
	GenerateSkill(ctx context.Context, prompt string) (operationID string, err error)

	// --- Workers (read-only) ---

	// ListWorkers returns all workers known to the service. The ordering is:
	// user workers alphabetically, then system workers alphabetically.
	ListWorkers(ctx context.Context) ([]Worker, error)

	// GetWorker returns a single worker by ID. Returns an error wrapping
	// ErrNotFound if the worker does not exist.
	GetWorker(ctx context.Context, id string) (Worker, error)

	// --- Graphs (read-only) ---

	// ListGraphs returns all loaded graph definitions, ordered by id.
	ListGraphs(ctx context.Context) ([]GraphDefinition, error)

	// GetGraph returns a single graph definition by id. Returns an error
	// wrapping ErrNotFound if the graph is not loaded.
	GetGraph(ctx context.Context, id string) (GraphDefinition, error)
}

// ---------------------------------------------------------------------------
// JobService
// ---------------------------------------------------------------------------

// JobService provides read-only access to jobs plus the ability to cancel them.
// Job creation is handled by the operator via its tool calls (create_job,
// create_task, assign_task) — not directly by the TUI.
type JobService interface {
	// List returns jobs matching the given filter. If filter is nil, all jobs
	// are returned. Results are ordered by creation time, newest first.
	// Pagination is supported via filter.Limit and filter.Offset.
	List(ctx context.Context, filter *JobListFilter) ([]Job, error)

	// ListAll returns all jobs regardless of status, ordered by creation time
	// newest first. Equivalent to List with an empty filter but no limit.
	// Used by the jobs modal which shows all historical jobs.
	ListAll(ctx context.Context) ([]Job, error)

	// Get returns a JobDetail for the given job ID, including its tasks and
	// recent progress reports. Returns an error wrapping ErrNotFound if the
	// job does not exist.
	Get(ctx context.Context, id string) (JobDetail, error)

	// Cancel cancels the job with the given ID by setting its status to
	// Cancelled. Only jobs in Active, Pending, SettingUp, or Decomposing
	// status can be cancelled; others return an error. The cancellation is
	// persisted to the DB immediately.
	Cancel(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// SessionService
// ---------------------------------------------------------------------------

// SessionService provides read-only access to worker sessions plus the ability
// to cancel them. Session creation is handled by the runtime when the operator
// assigns tasks to graphs.
type SessionService interface {
	// List returns all currently active worker sessions as snapshots. Snapshots
	// carry real-time token counts from the in-process runtime (not the DB
	// records, which are only written on session completion).
	List(ctx context.Context) ([]SessionSnapshot, error)

	// Get returns a full SessionDetail for the given session ID, including the
	// accumulated output buffer, activity history, system prompt, and initial
	// message. Used by the output modal and for reconnect hydration (B4 concern:
	// clients call this on reconnect to rebuild the runtimeSlot state).
	// Returns an error wrapping ErrNotFound if the session does not exist.
	Get(ctx context.Context, id string) (SessionDetail, error)

	// Cancel cancels the session with the given ID. The session's goroutine
	// will be interrupted at the next tool-call boundary. Returns an error if
	// the session does not exist or is already complete.
	Cancel(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// SystemService
// ---------------------------------------------------------------------------

// SystemService provides health checks, model listing, and MCP server status.
type SystemService interface {
	// Health returns the current health status of the service, including
	// version and uptime. Always succeeds for LocalService; RemoteClient
	// returns an error if the server is unreachable.
	Health(ctx context.Context) (HealthStatus, error)

	// ListModels returns all models available from the configured LLM providers.
	// Used by the TUI to populate the model selector and display the active
	// model in the sidebar. Returns an error if the provider is unreachable.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// ListMCPServers returns the connection status and tool inventory for all
	// configured MCP servers. Includes both connected and failed servers.
	ListMCPServers(ctx context.Context) ([]MCPServerStatus, error)

	// GetProgressState returns the current full progress state snapshot.
	// Used by the HTTP server for the GET /api/v1/progress endpoint,
	// enabling clients to hydrate their state on connect/reconnect.
	// For real-time updates, clients should subscribe to the SSE event stream.
	GetProgressState(ctx context.Context) (ProgressState, error)

	// GetLogs returns the contents of the application log file.
	// Returns ("", nil) if the log file does not exist.
	GetLogs(ctx context.Context) (string, error)

	// ListCatalogProviders returns the full provider/model catalog from models.dev.
	// The data is cached in memory and refreshed periodically. Returns nil (not an
	// error) if the catalog is unavailable (e.g. no network).
	ListCatalogProviders(ctx context.Context) ([]CatalogProvider, error)

	// AddProvider writes a provider YAML file to the providers/ directory.
	// The file watcher will pick it up and register the provider automatically.
	// Returns an error if a provider with the same ID already exists.
	AddProvider(ctx context.Context, entry AddProviderRequest) error

	// UpdateProvider overwrites an existing provider YAML file. Returns an error
	// if the provider does not exist.
	UpdateProvider(ctx context.Context, entry AddProviderRequest) error

	// ListConfiguredProviderIDs returns the IDs of all providers that have
	// YAML files in the providers/ directory (i.e. are configured locally).
	ListConfiguredProviderIDs(ctx context.Context) ([]string, error)

	// SetOperatorProvider sets the operator's provider and model in config.yaml
	// and starts the operator live if the provider is available.
	SetOperatorProvider(ctx context.Context, providerID string, model string) error

	// ListProviderModels returns the models available from a specific configured
	// provider, queried live from the provider's API.
	ListProviderModels(ctx context.Context, providerID string) ([]ModelInfo, error)

	// GetSettings returns the current user-editable runtime settings (values
	// sourced from config.yaml that the /settings surface exposes).
	GetSettings(ctx context.Context) (Settings, error)

	// UpdateSettings persists the given settings to config.yaml and applies
	// them to the live service (e.g. refreshing the prompt engine so new
	// worker runs use the updated values). Invalid enum values are rejected.
	UpdateSettings(ctx context.Context, s Settings) error
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrNotFound is returned by Get methods when the requested entity does not
// exist. Callers can check with errors.Is(err, service.ErrNotFound).
//
// Note: this is defined as a variable (not a type) so it can be used with
// errors.Is across package boundaries, including when wrapped with fmt.Errorf.
var ErrNotFound = errNotFound("not found")

type errNotFound string

func (e errNotFound) Error() string { return string(e) }
